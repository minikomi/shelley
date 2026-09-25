// Package transcription defines provider-neutral transcription model and
// execution contracts. Provider packages implement Provider and register an
// implementation with the server; this package performs no HTTP requests.
package transcription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Protocol identifies the upstream transcription wire protocol.
type Protocol string

const (
	ProtocolOpenAI   Protocol = "openai"
	ProtocolDeepgram Protocol = "deepgram"
)

// APIProfile identifies the transcription response contract used by an API.
type APIProfile string

const (
	APIProfileAuto           APIProfile = "auto"
	APIProfileOpenAIJSON     APIProfile = "openai-json"
	APIProfileOpenAIWhisper  APIProfile = "openai-whisper"
	APIProfileOpenAIDiarized APIProfile = "openai-diarized"
)

// RequestEncoding identifies how audio is represented in an OpenAI-compatible
// transcription request.
type RequestEncoding string

const (
	RequestEncodingAuto       RequestEncoding = "auto"
	RequestEncodingMultipart  RequestEncoding = "multipart"
	RequestEncodingBase64JSON RequestEncoding = "base64-json"
)

// Role identifies the output needed by Shelley's recording flow.
type Role string

const (
	RoleTranscript Role = "transcript"
	RoleTimecoded  Role = "timecoded"
)

// Model is the internal transcription model configuration. APIKey is secret
// and must never be serialized by an API representation.
type Model struct {
	ID                string
	DisplayName       string
	Protocol          Protocol
	APIProfile        APIProfile
	RequestEncoding   RequestEncoding
	Provider          string
	Endpoint          string
	APIKey            string `json:"-"`
	Model             string
	SupportsPrompted  bool // Optional context prompts, not transcript eligibility.
	SupportsTimecodes bool
	Managed           bool
	Source            string
}

// Supports reports whether the model can fill role.
func (m Model) Supports(role Role) bool {
	switch role {
	case RoleTranscript:
		return true
	case RoleTimecoded:
		return m.SupportsTimecodes
	default:
		return false
	}
}

// ValidateRole rejects unknown roles at API and storage boundaries.
func ValidateRole(role Role) error {
	switch role {
	case RoleTranscript, RoleTimecoded:
		return nil
	default:
		return fmt.Errorf("invalid transcription role %q", role)
	}
}

// ValidateProtocol rejects protocols for which Shelley has no provider
// contract.
func ValidateProtocol(protocol Protocol) error {
	switch protocol {
	case ProtocolOpenAI, ProtocolDeepgram:
		return nil
	default:
		return fmt.Errorf("invalid transcription protocol %q", protocol)
	}
}

// NormalizeAPIProfile supplies the inference profile for unspecified values.
// Deepgram has one fixed contract and therefore always uses auto.
func NormalizeAPIProfile(profile APIProfile, protocol Protocol) (APIProfile, error) {
	profile = APIProfile(strings.TrimSpace(string(profile)))
	if profile == "" {
		return APIProfileAuto, nil
	}
	switch profile {
	case APIProfileAuto, APIProfileOpenAIJSON, APIProfileOpenAIWhisper, APIProfileOpenAIDiarized:
	default:
		return "", fmt.Errorf("invalid transcription API profile %q", profile)
	}
	if protocol == ProtocolDeepgram {
		return APIProfileAuto, nil
	}
	return profile, nil
}

func NormalizeRequestEncoding(encoding RequestEncoding, protocol Protocol) (RequestEncoding, error) {
	encoding = RequestEncoding(strings.TrimSpace(string(encoding)))
	if encoding == "" {
		return RequestEncodingAuto, nil
	}
	switch encoding {
	case RequestEncodingAuto, RequestEncodingMultipart, RequestEncodingBase64JSON:
	default:
		return "", fmt.Errorf("invalid transcription request encoding %q", encoding)
	}
	if protocol == ProtocolDeepgram {
		return RequestEncodingAuto, nil
	}
	return encoding, nil
}

func InferOpenAIProfile(model string) APIProfile {
	basename := strings.ToLower(path.Base(strings.TrimSpace(model)))
	switch {
	case strings.Contains(basename, "diarize"):
		return APIProfileOpenAIDiarized
	case strings.Contains(basename, "whisper"):
		return APIProfileOpenAIWhisper
	case basename == "gpt-4o-transcribe",
		strings.HasPrefix(basename, "gpt-4o-transcribe-"),
		basename == "gpt-4o-mini-transcribe",
		strings.HasPrefix(basename, "gpt-4o-mini-transcribe-"),
		basename == "gpt-transcribe",
		strings.HasPrefix(basename, "gpt-transcribe-"):
		return APIProfileOpenAIJSON
	default:
		return APIProfileAuto
	}
}

func InferOpenAIRequestEncoding(model string) RequestEncoding {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "fish-audio/") {
		return RequestEncodingBase64JSON
	}
	return RequestEncodingMultipart
}

// ModelRef is safe durable provenance for a selected model. Queued jobs persist
// these refs so retries and recovery never re-resolve changed defaults. Revision
// fingerprints the non-secret wire destination and model.
type ModelRef struct {
	ID              string          `json:"model_id"`
	Protocol        Protocol        `json:"protocol"`
	APIProfile      APIProfile      `json:"api_profile,omitempty"`
	RequestEncoding RequestEncoding `json:"request_encoding,omitempty"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model_name"`
	Revision        string          `json:"revision"`
}

// Revision returns a non-secret fingerprint of the request route. Any protocol,
// endpoint, or wire-model edit invalidates queued references.
func (m Model) Revision() string {
	sum := sha256.Sum256([]byte(string(m.Protocol) + "\x00" + string(m.RequestEncoding) + "\x00" + strings.TrimSpace(m.Endpoint) + "\x00" + strings.TrimSpace(m.Model)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Ref returns the non-secret durable provenance for m.
func (m Model) Ref() ModelRef {
	return ModelRef{
		ID: m.ID, Protocol: m.Protocol, APIProfile: m.APIProfile, RequestEncoding: m.RequestEncoding,
		Provider: m.Provider, Model: m.Model, Revision: m.Revision(),
	}
}

// Request is one capability-specific provider operation.
type Request struct {
	Model         Model
	Role          Role
	RecordingPath string
	Prompt        string
}

// Result is a provider-neutral transcription result. Transcript requests return
// Transcript; timecoded providers return JSON Timecodes and may also return a
// transcript.
type Result struct {
	Transcript string
	Timecodes  json.RawMessage
}

// Provider is implemented independently for each wire protocol. OpenAI and
// Deepgram implementations share this exact contract; the core does not know
// how either provider performs HTTP.
type Provider interface {
	Protocol() Protocol
	Transcribe(context.Context, Request) (Result, error)
}

// ValidateResult enforces the result required for a capability.
func ValidateResult(role Role, result Result) error {
	switch role {
	case RoleTranscript:
		if strings.TrimSpace(result.Transcript) == "" {
			return fmt.Errorf("transcription returned an empty transcript")
		}
	case RoleTimecoded:
		if len(result.Timecodes) == 0 || !json.Valid(result.Timecodes) {
			return fmt.Errorf("timecoded transcription returned invalid JSON timecodes")
		}
	default:
		return fmt.Errorf("invalid transcription role %q", role)
	}
	return nil
}

// ModelError attributes an execution failure to one stable model and role.
type ModelError struct {
	Role    Role
	ModelID string
	Err     error
}

func (e *ModelError) Error() string {
	return fmt.Sprintf("transcription %s model %q: %v", e.Role, e.ModelID, e.Err)
}

func (e *ModelError) Unwrap() error { return e.Err }
