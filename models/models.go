package models

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models/modelsdev"
)

// Provider identifies an LLM upstream API family.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderFireworks Provider = "fireworks"
	ProviderGemini    Provider = "gemini"
	ProviderXAI       Provider = "xai"
	ProviderBuiltIn   Provider = "builtin"
)

// SourceCustomLabel is the label used for custom (DB-backed) models.
const SourceCustomLabel = "custom"

// Provider-default BARE base URLs (origins). API-protocol path suffixes
// like "/v1" or "/v1/messages" are appended by the per-API-type service
// factory in Model.Build — keeping that knowledge out of the catalog
// and out of any caller that hands a baseURL to Build.
const (
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultOpenAIBaseURL    = "https://api.openai.com"
	DefaultFireworksBaseURL = "https://api.fireworks.ai/inference"
	DefaultGeminiBaseURL    = "https://generativelanguage.googleapis.com"
	DefaultXAIBaseURL       = "https://api.x.ai"
)

// APIType identifies the wire protocol Shelley uses to talk to a model.
// Multiple APIType values can share a Provider (notably OpenAI: Responses
// API vs. Chat Completions).
type APIType string

const (
	APITypeAnthropicMessages APIType = "anthropic-messages"
	APITypeOpenAIResponses   APIType = "openai-responses"
	APITypeOpenAIChat        APIType = "openai-chat-completions"
	APITypeGemini            APIType = "gemini"
	APITypeBuiltIn           APIType = "builtin"
)

// Model is one entry in Shelley's catalog of built-in models.
type Model struct {
	// ID is the user-facing identifier.
	ID string

	// Provider is the upstream API family.
	Provider Provider

	// Description is a human-readable description.
	Description string

	// APIModelName is the model name sent on the wire (e.g. "claude-opus-4-7").
	// Also used to match against an LLM integration's /v1/models allow-list.
	APIModelName string

	// APIType identifies the wire protocol used to talk to this model.
	APIType APIType

	// DefaultBaseURL is the base URL the provider package uses when no
	// explicit URL is configured. Shown in `shelley models` so users can
	// see exactly which endpoint each model will be reached at.
	DefaultBaseURL string

	// Build constructs an llm.Service for this model given a BARE base
	// URL (origin + any non-API prefix, e.g. "https://llm.int.exe.xyz"
	// or "" for the provider package default), an API key, and an HTTP
	// client. The function is responsible for appending its own
	// API-protocol path ("/v1", "/v1/messages", "/v1beta", ...) — the
	// caller never encodes those.
	Build func(baseURL, apiKey string, httpc *http.Client) llm.Service
}

// Built is a ready-to-use model, shaped to mirror a row in the custom
// models database table. The Manager treats built-in and custom models
// uniformly via this struct.
type Built struct {
	ID          string
	DisplayName string
	Provider    Provider
	Mode        string
	Source      string // human-readable origin ("exe.dev gateway", "$ANTHROPIC_API_KEY", "custom", ...)
	ReleaseDate string // ISO date from models.dev; empty when unknown
	Service     llm.Service

	// APIModelName is the model name sent on the wire, used for models.dev
	// lookups (see Model.APIModelName).
	APIModelName string

	// APIType is the wire protocol used to talk to this model.
	APIType APIType

	// BaseURL is the resolved upstream base URL (after applying any source
	// override on top of the catalog's DefaultBaseURL).
	BaseURL string

	// SourceID identifies a saved provider connection. It is empty for
	// built-in, integration, and custom models.
	SourceID string

	// Capabilities contains provider-aware catalog metadata when available.
	Capabilities *modelsdev.Metadata

	serviceRevision uint64
}

// ProviderOutputTokenCap is the conservative output allowance for saved
// provider routes whose output limit is missing or advisory. Advisory catalog
// matches may describe a different model behind a proxy, so they never raise
// it. An authoritative output_limit (provider-reported or exact models.dev)
// is used as-is, including above this value; the wire services still lower
// it to any known catalog limit.
const ProviderOutputTokenCap = 8192

// providerOutputCap returns the max output tokens for a saved provider route.
func providerOutputCap(capabilities *modelsdev.Metadata) int {
	if capabilities.Authoritative("output_limit") && capabilities.OutputLimit > 0 {
		return capabilities.OutputLimit
	}
	return ProviderOutputTokenCap
}

// BuildProviderModel creates a runtime model for one explicitly configured
// saved provider route. endpoint is the complete API base (including its
// version/custom prefix); apiKey is always passed explicitly to the transport.
func BuildProviderModel(providerID, upstreamID, runtimeID, displayName, providerType, endpoint, apiKey string, httpc *http.Client, capabilities *modelsdev.Metadata) (Built, error) {
	var (
		apiType  APIType
		provider Provider
		service  llm.Service
	)
	images := capabilities.Authoritative("images") && capabilities.Images != nil && *capabilities.Images
	reasoning := providerReasoningEnabled(capabilities)
	outputCap := providerOutputCap(capabilities)

	switch providerType {
	case "anthropic":
		apiType, provider = APITypeAnthropicMessages, ProviderAnthropic
		svc := &ant.Service{
			URL: endpoint + "/messages", APIKey: apiKey, Model: upstreamID,
			HTTPC: httpc, MaxTokens: outputCap, ThinkingLevel: llm.ThinkingLevelOff,
			SupportsImages_: images, EnableThinkingBinding: reasoning,
			DisableReasoning: !reasoning,
		}
		if capabilities != nil {
			if capabilities.Provenance["reasoning_levels"] == "provider" {
				svc.ReasoningLevels = append([]llm.ThinkingLevel{}, capabilities.ReasoningLevels...)
			}
			if capabilities.Provenance["thinking_types"] == "provider" {
				svc.ThinkingTypes = append([]string{}, capabilities.ThinkingTypes...)
			}
		}
		service = providerCapabilityService{Service: svc, metadata: capabilities}
	case "openai":
		apiType, provider = APITypeOpenAIChat, ProviderOpenAI
		model := oai.Model{UserName: upstreamID, ModelName: upstreamID, URL: endpoint, SupportsImages: images}
		// Reasoning replay stays on the adapter's auto policy: it is wire
		// state for tool continuations, independent of reasoning controls.
		svc := &oai.Service{
			Model: model, ModelURL: endpoint, APIKey: apiKey, HTTPC: httpc,
			MaxTokens: outputCap, ProviderName: "openai", ThinkingLevel: llm.ThinkingLevelOff,
			DisableReasoning: !reasoning,
		}
		service = providerCapabilityService{Service: svc, metadata: capabilities}
	case "openai-responses":
		apiType, provider = APITypeOpenAIResponses, ProviderOpenAI
		model := oai.Model{UserName: upstreamID, ModelName: upstreamID, URL: endpoint, SupportsImages: images}
		if catalog, ok := officialResponsesModel(upstreamID, capabilities); ok {
			model.TextVerbosity, model.SupportsApplyPatch = catalog.TextVerbosity, catalog.SupportsApplyPatch
		}
		svc := &oai.ResponsesService{
			Model: model, ModelURL: endpoint, APIKey: apiKey, HTTPC: httpc,
			MaxTokens: outputCap, ProviderName: "openai", ThinkingLevel: llm.ThinkingLevelOff,
			DisableReasoning: !reasoning,
		}
		service = providerCapabilityService{Service: svc, metadata: capabilities}
	default:
		return Built{}, fmt.Errorf("unsupported provider type %q", providerType)
	}
	if displayName == "" {
		displayName = upstreamID
	}
	identity, err := json.Marshal(struct {
		ProviderType, Endpoint, APIKey, UpstreamID string
		Capabilities                               *modelsdev.Metadata
	}{providerType, endpoint, apiKey, upstreamID, capabilities})
	if err != nil {
		return Built{}, err
	}
	digest := sha256.Sum256(identity)
	var releaseDate string
	if capabilities != nil {
		releaseDate = capabilities.ReleaseDate
	}
	return Built{
		ID: runtimeID, DisplayName: displayName, Provider: provider,
		Source: displayName, ReleaseDate: releaseDate, Service: service,
		APIModelName: upstreamID, APIType: apiType, BaseURL: endpoint,
		SourceID: providerID, Capabilities: cloneProviderMetadata(capabilities),
		serviceRevision: binary.BigEndian.Uint64(digest[:8]),
	}, nil
}

// officialResponsesModel returns the built-in catalog's Responses model
// settings (text verbosity, apply_patch training) for a wire ID that exact
// models.dev evidence ties to OpenAI's own endpoint.
func officialResponsesModel(upstreamID string, capabilities *modelsdev.Metadata) (oai.Model, bool) {
	if capabilities == nil || capabilities.Match != "exact" || capabilities.ProviderID != "openai" {
		return oai.Model{}, false
	}
	for _, m := range All() {
		if m.Provider == ProviderOpenAI && m.APIType == APITypeOpenAIResponses && m.APIModelName == upstreamID {
			if svc, ok := m.Build("", "", nil).(*oai.ResponsesService); ok {
				return svc.Model, true
			}
		}
	}
	return oai.Model{}, false
}

type providerCapabilityService struct {
	llm.Service
	metadata *modelsdev.Metadata
}

func (s providerCapabilityService) PatchProfile() string { return llm.PatchProfile(s.Service) }

func (s providerCapabilityService) SupportsImages() bool {
	return s.metadata.Authoritative("images") && s.metadata.Images != nil && *s.metadata.Images
}

func (s providerCapabilityService) SupportsReasoning() bool {
	return providerReasoningEnabled(s.metadata)
}

func providerReasoningEnabled(metadata *modelsdev.Metadata) bool {
	// Reasoning support alone does not establish a model's wire vocabulary.
	return metadata != nil && metadata.Match == "exact" &&
		(metadata.Provenance["thinking_types"] != "provider" || len(metadata.ThinkingTypes) > 0) &&
		metadata.Authoritative("reasoning") && metadata.Reasoning != nil && *metadata.Reasoning &&
		metadata.Authoritative("reasoning_levels") && len(metadata.ReasoningLevels) > 0
}

func (s providerCapabilityService) SupportedReasoningLevels() []llm.ThinkingLevel {
	if !providerReasoningEnabled(s.metadata) {
		return []llm.ThinkingLevel{}
	}
	return append([]llm.ThinkingLevel(nil), s.metadata.ReasoningLevels...)
}

func (s providerCapabilityService) DefaultReasoningLevel() string {
	if !s.SupportsReasoning() {
		return ""
	}
	return llm.ServiceDefaultReasoningLevel(s.Service)
}

func (s providerCapabilityService) SupportsServerSideWebSearch() bool { return false }

func (s providerCapabilityService) CacheTTL() time.Duration { return llm.CacheTTL(s.Service) }

func (s providerCapabilityService) WarmCache(ctx context.Context, request *llm.Request) (llm.Usage, error) {
	return llm.WarmCache(ctx, s.Service, s.prepareRequest(request))
}

func (s providerCapabilityService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	return s.Service.Do(ctx, s.prepareRequest(request))
}

func (s providerCapabilityService) prepareRequest(request *llm.Request) *llm.Request {
	if !providerReasoningEnabled(s.metadata) {
		copyRequest := *request
		copyRequest.ThinkingLevel = llm.ThinkingLevelOff
		copyRequest.ReasoningEffort = ""
		request = &copyRequest
	}
	return request
}

func cloneProviderMetadata(value *modelsdev.Metadata) *modelsdev.Metadata {
	if value == nil {
		return nil
	}
	copy := *value
	copy.InputModalities = append([]string(nil), value.InputModalities...)
	copy.OutputModalities = append([]string(nil), value.OutputModalities...)
	copy.ThinkingTypes = append([]string(nil), value.ThinkingTypes...)
	copy.ReasoningLevels = append([]llm.ThinkingLevel(nil), value.ReasoningLevels...)
	if value.Provenance != nil {
		copy.Provenance = make(map[string]string, len(value.Provenance))
		for field, source := range value.Provenance {
			copy.Provenance[field] = source
		}
	}
	if value.ToolCall != nil {
		v := *value.ToolCall
		copy.ToolCall = &v
	}
	if value.Images != nil {
		v := *value.Images
		copy.Images = &v
	}
	if value.Reasoning != nil {
		v := *value.Reasoning
		copy.Reasoning = &v
	}
	return &copy
}

// OpenAI transcription models used for recordings: TranscriptionTextModel
// produces the canonical transcript, TranscriptionTimestampsModel the word and
// segment timings.
const (
	TranscriptionTextModel       = "gpt-transcribe"
	TranscriptionTimestampsModel = "whisper-1"
)

// TranscriptionModel is a known OpenAI-compatible transcription route.
type TranscriptionModel struct {
	Model    string
	Endpoint string
	APIKey   string
	Source   string
	// FromCredentials marks a route derived from OpenAI credentials (env key
	// or gateway). Integration and custom routes take precedence over it.
	FromCredentials bool
}

// Config holds runtime configuration for the Manager. Built-in models
// are passed in pre-materialized; custom models are loaded from DB.
type Config struct {
	// Models is the set of ready-to-use built-in models, in display order.
	Models []Built

	// TranscriptionModels are non-chat models discovered from integrations.
	TranscriptionModels []TranscriptionModel

	Logger *slog.Logger

	// DB holds custom models; optional.
	DB *db.DB

	// HTTPC is the shared HTTP client used to back custom models loaded
	// from DB. If nil, a default llmhttp client is created.
	HTTPC *http.Client
}

// --- Catalog ---------------------------------------------------------------

// antSvc / oaiResponsesSvc / oaiChatSvc / gemSvc are factories for the
// per-provider llm.Service constructors used by Model.Build.
//
// The `baseURL` parameter is a BARE origin/prefix with NO API-protocol
// path on it (e.g. "https://llm.int.exe.xyz" or "" for the package
// default). Each factory knows its own protocol's path suffix and
// appends it before constructing the service. Keeping that knowledge
// inside the factory means modelsources never has to encode
// per-API-type paths like "/v1" or "/v1/messages".
func antSvc(modelName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &ant.Service{APIKey: apiKey, Model: modelName, HTTPC: httpc, ThinkingLevel: llm.ThinkingLevelMedium, SupportsImages_: true, EnableThinkingBinding: true}
		if baseURL != "" {
			s.URL = baseURL + "/v1/messages"
		}
		return s
	}
}

// outputLimit is the models.dev output limit for a model, or 0 so the service
// falls back to its own default. Uses the configured base URL when set,
// otherwise the model's canonical URL.
func outputLimit(baseURL, modelURL, modelName string) int {
	limit, _ := modelsdev.LookupOutputLimit(cmp.Or(baseURL, modelURL), modelName)
	return limit
}

func oaiResponsesSvc(model oai.Model) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return oaiResponsesSvcNamed(model, "openai")
}

func oaiResponsesSvcNamed(model oai.Model, providerName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &oai.ResponsesService{
			Model: model, APIKey: apiKey, HTTPC: httpc,
			MaxTokens:     outputLimit(baseURL, model.URL, model.ModelName),
			ThinkingLevel: llm.ThinkingLevelMedium, ProviderName: providerName,
		}
		if baseURL != "" {
			s.ModelURL = baseURL + "/v1"
		}
		return s
	}
}

func oaiChatSvc(model oai.Model, providerName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &oai.Service{
			Model: model, APIKey: apiKey, HTTPC: httpc,
			MaxTokens: outputLimit(baseURL, model.URL, model.ModelName), ProviderName: providerName,
		}
		if baseURL != "" {
			s.ModelURL = baseURL + "/v1"
		}
		return s
	}
}

func gemSvc(modelName string) func(baseURL, apiKey string, httpc *http.Client) llm.Service {
	return func(baseURL, apiKey string, httpc *http.Client) llm.Service {
		s := &gem.Service{APIKey: apiKey, Model: modelName, MaxTokens: outputLimit(baseURL, "", modelName), HTTPC: httpc, SupportsImages_: true}
		if baseURL != "" {
			s.URL = baseURL + "/v1beta"
		}
		return s
	}
}

// All returns all available models in Shelley.
//
// Order is significant: it is the display order in the model picker and, when
// no default is configured, the first ready model is the default. Integrations
// supply their own ordered catalogs instead of using this order.
//
// Models are organized by "family" — the usual notion of a model lineage from
// one provider/trainer (the "Opus" line, the "GPT-5" line, and so on).
//
// Only the newest release in a family holds that family's flagship slot near
// the top of the list. Older releases in the same family are obviated by the
// newer one and drop into the secondary group lower down (they stay selectable,
// just deprioritized). A different family is never obviated by a higher-numbered
// release of another family, so each family keeps its own flagship slot.
//
// There is one surprising wrinkle: Opus <= 4.6 and Opus >= 4.7 are treated as
// two separate families even though both are "Opus". The reason is the
// tokenizer. Opus 4.7 introduced a new tokenizer (inherited by 4.8) that emits
// more tokens for the same text — the per-token rates are identical across 4.6,
// 4.7, and 4.8, but the same prompt costs more under 4.7/4.8. That difference is
// large enough that we keep 4.6 in its own flagship slot rather than letting it
// be obviated by 4.7/4.8.
//
// When adding a newer release of an existing family, put it in the family's
// flagship slot and move the prior release down into the secondary group.
func All() []Model {
	return []Model{
		{
			ID: "claude-opus-5.5", Provider: ProviderAnthropic,
			Description: "Claude Opus 5.5", APIModelName: ant.Claude55Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude55Opus),
		},
		{
			ID: "claude-sonnet-5.5", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 5.5", APIModelName: ant.Claude55Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude55Sonnet),
		},
		{
			ID: "claude-haiku-5.5", Provider: ProviderAnthropic,
			Description: "Claude Haiku 5.5", APIModelName: ant.Claude55Haiku,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude55Haiku),
		},
		{
			ID: "claude-opus-5", Provider: ProviderAnthropic,
			Description: "Claude Opus 5", APIModelName: ant.Claude5Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude5Opus),
		},
		{
			ID: "claude-fable-5.1", Provider: ProviderAnthropic,
			Description: "Claude Fable 5.1", APIModelName: ant.ClaudeFable51,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.ClaudeFable51),
		},
		{
			ID: "claude-fable-5", Provider: ProviderAnthropic,
			Description: "Claude Fable 5", APIModelName: ant.ClaudeFable5,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.ClaudeFable5),
		},
		{
			ID: "gpt-6-astra", Provider: ProviderOpenAI,
			Description: "GPT-6 Astra", APIModelName: oai.GPT6Astra.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT6Astra),
		},
		{
			ID: "gpt-6.1-sol", Provider: ProviderOpenAI,
			Description: "GPT-6.1 Sol", APIModelName: oai.GPT61Sol.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT61Sol),
		},
		{
			ID: "gpt-6-sol", Provider: ProviderOpenAI,
			Description: "GPT-6 Sol", APIModelName: oai.GPT6Sol.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT6Sol),
		},
		{
			ID: "gpt-6-luna", Provider: ProviderOpenAI,
			Description: "GPT-6 Luna", APIModelName: oai.GPT6Luna.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT6Luna),
		},
		{
			ID: "gpt-5.6-sol", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Sol", APIModelName: oai.GPT56Sol.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Sol),
		},
		{
			ID: "gpt-5.6-terra", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Terra", APIModelName: oai.GPT56Terra.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Terra),
		},
		{
			ID: "gpt-5.6-luna", Provider: ProviderOpenAI,
			Description: "GPT-5.6 Luna", APIModelName: oai.GPT56Luna.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT56Luna),
		},
		{
			ID: "gpt-5.5", Provider: ProviderOpenAI,
			Description: "GPT-5.5", APIModelName: oai.GPT55.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT55),
		},
		{
			ID: "claude-opus-4.6", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.6", APIModelName: ant.Claude46Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude46Opus),
		},
		{
			ID: "glm-5.3-fireworks", Provider: ProviderFireworks,
			Description: "GLM-5.3 on Fireworks", APIModelName: oai.GLM53Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GLM53Fireworks, "fireworks"),
		},
		{
			ID: "glm-5.3-flash-fireworks", Provider: ProviderFireworks,
			Description: "GLM-5.3 Flash on Fireworks", APIModelName: oai.GLM53FlashFireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GLM53FlashFireworks, "fireworks"),
		},
		{
			ID: "gemini-3.1-pro", Provider: ProviderGemini,
			Description: "Gemini 3.1 Pro", APIModelName: "gemini-3.1-pro-preview",
			APIType: APITypeGemini, DefaultBaseURL: DefaultGeminiBaseURL,
			Build: gemSvc("gemini-3.1-pro-preview"),
		},
		{
			ID: "gemini-3.8-flash", Provider: ProviderGemini,
			Description: "Gemini 3.8 Flash", APIModelName: "gemini-3.8-flash",
			APIType: APITypeGemini, DefaultBaseURL: DefaultGeminiBaseURL,
			Build: gemSvc("gemini-3.8-flash"),
		},
		{
			ID: "grok-4.5", Provider: ProviderXAI,
			Description: "Grok 4.5", APIModelName: oai.Grok45.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultXAIBaseURL,
			Build: oaiResponsesSvcNamed(oai.Grok45, "xai"),
		},
		{
			ID: "kimi-k3-fireworks", Provider: ProviderFireworks,
			Description: "Kimi K3 on Fireworks", APIModelName: oai.KimiK3Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.KimiK3Fireworks, "fireworks"),
		},
		{
			ID: "claude-opus-4.8", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.8 (default)", APIModelName: ant.Claude48Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude48Opus),
		},
		{
			ID: "claude-opus-4.7", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.7", APIModelName: ant.Claude47Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude47Opus),
		},
		{
			ID: "claude-opus-4.5", Provider: ProviderAnthropic,
			Description: "Claude Opus 4.5", APIModelName: ant.Claude45Opus,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Opus),
		},
		{
			ID: "claude-sonnet-5", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 5", APIModelName: ant.Claude5Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude5Sonnet),
		},
		{
			ID: "claude-sonnet-4.6", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 4.6", APIModelName: ant.Claude46Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude46Sonnet),
		},
		{
			ID: "claude-sonnet-4.5", Provider: ProviderAnthropic,
			Description: "Claude Sonnet 4.5", APIModelName: ant.Claude45Sonnet,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Sonnet),
		},
		{
			ID: "claude-haiku-4.5", Provider: ProviderAnthropic,
			Description: "Claude Haiku 4.5", APIModelName: ant.Claude45Haiku,
			APIType: APITypeAnthropicMessages, DefaultBaseURL: DefaultAnthropicBaseURL,
			Build: antSvc(ant.Claude45Haiku),
		},
		{
			ID: "gpt-5.4", Provider: ProviderOpenAI,
			Description: "GPT-5.4", APIModelName: oai.GPT54.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54),
		},
		{
			ID: "gpt-5.4-mini", Provider: ProviderOpenAI,
			Description: "GPT-5.4 mini", APIModelName: oai.GPT54Mini.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54Mini),
		},
		{
			ID: "gpt-5.4-nano", Provider: ProviderOpenAI,
			Description: "GPT-5.4 nano", APIModelName: oai.GPT54Nano.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT54Nano),
		},
		{
			ID: "gpt-5.3-codex", Provider: ProviderOpenAI,
			Description: "GPT-5.3 Codex", APIModelName: oai.GPT53Codex.ModelName,
			APIType: APITypeOpenAIResponses, DefaultBaseURL: DefaultOpenAIBaseURL,
			Build: oaiResponsesSvc(oai.GPT53Codex),
		},
		{
			ID: "deepseek-v4.1-flash-fireworks", Provider: ProviderFireworks,
			Description: "DeepSeek V4.1 Flash on Fireworks", APIModelName: oai.DeepseekV41FlashFireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.DeepseekV41FlashFireworks, "fireworks"),
		},
		{
			ID: "glm-5.2-fireworks", Provider: ProviderFireworks,
			Description: "GLM-5.2 on Fireworks", APIModelName: oai.GLM52Fireworks.ModelName,
			APIType: APITypeOpenAIChat, DefaultBaseURL: DefaultFireworksBaseURL,
			Build: oaiChatSvc(oai.GLM52Fireworks, "fireworks"),
		},
		{
			ID: "predictable", Provider: ProviderBuiltIn,
			Description:    "Deterministic test model (no API key)",
			APIType:        APITypeBuiltIn,
			DefaultBaseURL: "-",
			Build:          func(url, apiKey string, httpc *http.Client) llm.Service { return predictable.NewService() },
		},
	}
}

// ByID returns the model with the given ID, or nil if not found.
func ByID(id string) *Model {
	for _, m := range All() {
		if m.ID == id {
			return &m
		}
	}
	return nil
}

// IDs returns all catalog model IDs.
func IDs() []string {
	models := All()
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// Default returns the default catalog model.
func Default() Model {
	if m := ByID("claude-opus-4.8"); m != nil {
		return *m
	}
	return All()[0]
}

// --- Manager ---------------------------------------------------------------

// Manager owns the live set of LLM services for a Shelley server.
type Manager struct {
	mu                  sync.RWMutex
	services            map[string]serviceEntry
	modelOrder          []string
	transcriptionModels []TranscriptionModel
	providerRevisions   map[string]uint64
	// providerEpoch increases with every provider revision.
	providerEpoch   uint64
	resolveProvider func(modelID string) (Built, error)
	logger          *slog.Logger
	db              *db.DB
	httpc           *http.Client
}

// GetWorkhorseService returns a service that uses a cheap model from the
// conversation model's provider, falling back to the conversation model once.
func (m *Manager) GetWorkhorseService(conversationModelID string) (llm.Service, error) {
	return m.getWorkhorseService(conversationModelID)
}

type serviceEntry struct {
	service     llm.Service
	provider    Provider
	modelID     string
	mode        string
	source      string
	displayName string
	releaseDate string
	baseURL     string
	apiType     APIType
	// apiModelName is the wire model name used for models.dev lookups.
	apiModelName string
	sourceID     string
	capabilities *modelsdev.Metadata
	incompatible bool

	serviceRevision uint64
}

// ConfigInfo is an optional interface that services can implement to provide configuration details for logging
type ConfigInfo interface {
	ConfigDetails() map[string]string
}

// loggingService wraps an llm.Service with request/usage logging.
type loggingService struct {
	service  llm.Service
	logger   *slog.Logger
	modelID  string
	provider Provider
}

func (l *loggingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	start := time.Now()
	ctx = llmhttp.WithModelID(ctx, l.modelID)
	ctx = llmhttp.WithProvider(ctx, string(l.provider))
	response, err := l.service.Do(ctx, request)
	durationSeconds := time.Since(start).Seconds()

	if err != nil {
		logAttrs := []any{"model", l.modelID, "duration_seconds", durationSeconds}
		if configProvider, ok := l.service.(ConfigInfo); ok {
			for k, v := range configProvider.ConfigDetails() {
				logAttrs = append(logAttrs, k, v)
			}
		}
		logAttrs = append(logAttrs, "error", err)
		l.logger.Error("LLM request failed", logAttrs...)
		return response, err
	}

	logAttrs := []any{"model", l.modelID, "duration_seconds", durationSeconds}
	if !response.Usage.IsZero() {
		logAttrs = append(
			logAttrs,
			"input_tokens", response.Usage.InputTokens,
			"output_tokens", response.Usage.OutputTokens,
			"cost_usd", response.Usage.CostUSD,
		)
		if response.Usage.CacheCreationInputTokens > 0 {
			logAttrs = append(logAttrs, "cache_creation_input_tokens", response.Usage.CacheCreationInputTokens)
		}
		if response.Usage.CacheReadInputTokens > 0 {
			logAttrs = append(logAttrs, "cache_read_input_tokens", response.Usage.CacheReadInputTokens)
		}
	}
	l.logger.Info("LLM request completed", logAttrs...)
	if purpose := llm.PurposeFromContext(ctx); purpose != "" && !response.Usage.IsZero() {
		if collect := llm.UsageCollectorFromContext(ctx); collect != nil {
			usage := response.UsageWithMeta()
			if usage.Model == "" {
				usage.Model = l.modelID
			}
			collect(purpose, usage)
		}
	}
	return response, err
}

func (l *loggingService) CacheTTL() time.Duration { return llm.CacheTTL(l.service) }

func (l *loggingService) WarmCache(ctx context.Context, request *llm.Request) (llm.Usage, error) {
	ctx = llmhttp.WithModelID(ctx, l.modelID)
	ctx = llmhttp.WithProvider(ctx, string(l.provider))
	usage, err := llm.WarmCache(ctx, l.service, request)
	if err != nil {
		return usage, err
	}
	if usage.Model == "" {
		usage.Model = l.modelID
	}
	l.logger.Info("LLM prompt cache warmed", "model", l.modelID, "input_tokens", usage.InputTokens,
		"cache_read_input_tokens", usage.CacheReadInputTokens,
		"cache_creation_input_tokens", usage.CacheCreationInputTokens, "cost_usd", usage.CostUSD)
	return usage, nil
}

func (l *loggingService) Provider() string       { return l.service.Provider() }
func (l *loggingService) MaxImageDimension() int { return l.service.MaxImageDimension() }
func (l *loggingService) MaxImageBytes() int     { return l.service.MaxImageBytes() }

func (l *loggingService) PatchProfile() string { return llm.PatchProfile(l.service) }

func (l *loggingService) SupportsServerSideWebSearch() bool {
	type capable interface{ SupportsServerSideWebSearch() bool }
	if c, ok := l.service.(capable); ok {
		return c.SupportsServerSideWebSearch()
	}
	return false
}

func (l *loggingService) SupportsImages() bool { return l.service.SupportsImages() }
func (l *loggingService) SupportsReasoning() bool {
	return llm.SupportsReasoning(l.service)
}

func (l *loggingService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return llm.SupportedReasoningLevels(l.service)
}

// DefaultReasoningLevel forwards the wrapped service's default reasoning level
// so the llm.DefaultReasoner assertion survives the logging wrapper.
func (l *loggingService) DefaultReasoningLevel() string {
	return llm.ServiceDefaultReasoningLevel(l.service)
}

// NewManager registers the supplied built-in models, then loads custom
// models from cfg.DB.
func NewManager(cfg *Config) (*Manager, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	httpc := cfg.HTTPC
	if httpc == nil {
		httpc = llmhttp.NewClient(nil)
	}
	m := &Manager{
		services:            map[string]serviceEntry{},
		providerRevisions:   map[string]uint64{},
		transcriptionModels: append([]TranscriptionModel(nil), cfg.TranscriptionModels...),
		logger:              cfg.Logger,
		db:                  cfg.DB,
		httpc:               httpc,
	}

	m.registerBuiltModelsLocked(cfg.Models)

	if err := m.loadCustomModels(); err != nil && cfg.Logger != nil {
		cfg.Logger.Warn("Failed to load custom models", "error", err)
	}
	return m, nil
}

func newServiceEntry(b Built) serviceEntry {
	return serviceEntry{
		service:      b.Service,
		provider:     b.Provider,
		modelID:      b.ID,
		mode:         b.Mode,
		source:       b.Source,
		displayName:  cmp.Or(b.DisplayName, b.ID),
		releaseDate:  b.ReleaseDate,
		baseURL:      b.BaseURL,
		apiType:      b.APIType,
		apiModelName: b.APIModelName,
		sourceID:     b.SourceID,
		capabilities: cloneProviderMetadata(b.Capabilities),
		incompatible: b.SourceID != "" && KnownAgentIncompatible(b.Capabilities),

		serviceRevision: b.serviceRevision,
	}
}

func (m *Manager) registerBuiltModelsLocked(built []Built) {
	for _, b := range built {
		m.services[b.ID] = newServiceEntry(b)
		m.modelOrder = append(m.modelOrder, b.ID)
		if m.logger != nil {
			// Saved providers can publish hundreds of routes per refresh.
			level := slog.LevelInfo
			if b.SourceID != "" {
				level = slog.LevelDebug
			}
			m.logger.Log(context.Background(), level, "Registered model", "id", b.ID, "source", b.Source)
		}
	}
}

func (m *Manager) customModelRows() ([]generated.Model, error) {
	if m.db == nil {
		return nil, nil
	}
	return m.db.GetModels(context.Background())
}

func (m *Manager) loadCustomModels() error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.loadCustomModelsLocked(dbModels)
	return nil
}

func (m *Manager) loadCustomModelsLocked(dbModels []generated.Model) {
	for _, model := range dbModels {
		if _, exists := m.services[model.ModelID]; exists {
			continue
		}
		svc := m.createServiceFromModel(&model)
		if svc == nil {
			continue
		}
		m.services[model.ModelID] = serviceEntry{
			service:      svc,
			provider:     Provider(model.ProviderType),
			modelID:      model.ModelID,
			source:       SourceCustomLabel,
			displayName:  model.DisplayName,
			baseURL:      model.Endpoint,
			apiModelName: model.ModelName,
		}
		m.modelOrder = append(m.modelOrder, model.ModelID)
	}
}

// RefreshCustomModels reloads custom models from the database. Call this
// after adding or removing custom models via the UI.
func (m *Manager) RefreshCustomModels() error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previousOrder := m.modelOrder
	hasSaved := m.hasProviderRoutesLocked()
	newOrder := make([]string, 0, len(m.modelOrder))
	for _, id := range m.modelOrder {
		entry, ok := m.services[id]
		if ok && (entry.sourceID != "" || entry.source != SourceCustomLabel) {
			newOrder = append(newOrder, id)
		} else {
			delete(m.services, id)
		}
	}
	m.modelOrder = newOrder
	m.loadCustomModelsLocked(dbModels)
	// Only a catalog with saved routes needs its order kept stable; without
	// any, the order is exactly the baseline's.
	if hasSaved {
		preserveModelOrder(m.modelOrder, previousOrder)
	}
	return nil
}

func (m *Manager) hasProviderRoutesLocked() bool {
	for _, entry := range m.services {
		if entry.sourceID != "" {
			return true
		}
	}
	return false
}

func preserveModelOrder(order, previous []string) {
	rank := make(map[string]int, len(previous))
	for i, id := range previous {
		rank[id] = i
	}
	position := func(id string) int {
		if i, ok := rank[id]; ok {
			return i
		}
		return len(previous)
	}
	slices.SortStableFunc(order, func(a, b string) int { return position(a) - position(b) })
}

// RefreshBuiltModels replaces the non-custom models with a freshly discovered
// built-in/catalog set, then re-applies DB-backed custom models.
func (m *Manager) RefreshBuiltModels(built []Built) error {
	dbModels, err := m.customModelRows()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	providerBuilt := make([]Built, 0)
	for _, id := range m.modelOrder {
		entry, ok := m.services[id]
		if !ok || entry.sourceID == "" {
			continue
		}
		providerBuilt = append(providerBuilt, Built{
			ID: entry.modelID, DisplayName: entry.displayName, Provider: entry.provider,
			Mode: entry.mode, Source: entry.source,
			ReleaseDate: entry.releaseDate, Service: entry.service,
			APIModelName: entry.apiModelName, APIType: entry.apiType,
			BaseURL: entry.baseURL, SourceID: entry.sourceID,
			Capabilities: entry.capabilities, serviceRevision: entry.serviceRevision,
		})
	}
	var previousOrder []string
	if len(providerBuilt) > 0 {
		for _, id := range m.modelOrder {
			if entry, ok := m.services[id]; ok && (entry.sourceID != "" || entry.source == SourceCustomLabel) {
				previousOrder = append(previousOrder, id)
			}
		}
	}
	m.services = map[string]serviceEntry{}
	m.modelOrder = nil
	m.registerBuiltModelsLocked(built)
	head := len(m.modelOrder)
	m.registerBuiltModelsLocked(providerBuilt)
	m.loadCustomModelsLocked(dbModels)
	// With saved routes, they and individual custom routes keep their
	// relative order (so the first-ready default account cannot flip);
	// genuinely new routes follow. Without any, the order is the baseline's.
	if len(providerBuilt) > 0 {
		preserveModelOrder(m.modelOrder[head:], previousOrder)
	}
	return nil
}

// RelabelProviderModels changes the display source of one saved provider's
// routes. It never alters services, order or the provider revision.
func (m *Manager) RelabelProviderModels(providerID, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, entry := range m.services {
		if entry.sourceID == providerID {
			entry.source = source
			m.services[id] = entry
		}
	}
}

// ReplaceProviderModels atomically replaces the complete in-memory catalog for
// one saved provider source. Callers must materialize and validate all models
// before persistence; this method deliberately has no fallible work.
func (m *Manager) ReplaceProviderModels(providerID string, built []Built) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.services == nil {
		m.services = make(map[string]serviceEntry)
	}
	if m.providerRevisions == nil {
		m.providerRevisions = make(map[string]uint64)
	}
	// Replace in place: the source keeps the position of its first existing
	// route, so a no-op refresh cannot change which source comes first (and
	// so which account is the default). A source with no routes appends.
	newOrder := make([]string, 0, len(m.modelOrder)+len(built))
	anchor := -1
	for _, id := range m.modelOrder {
		entry, ok := m.services[id]
		if ok && entry.sourceID != providerID {
			newOrder = append(newOrder, id)
		} else if ok {
			if anchor < 0 {
				anchor = len(newOrder)
			}
			delete(m.services, id)
		}
	}
	m.modelOrder = newOrder
	prefix := len(m.modelOrder)
	seen := make(map[string]struct{}, len(m.services))
	for id := range m.services {
		seen[id] = struct{}{}
	}
	for _, model := range built {
		if model.SourceID != providerID {
			model.SourceID = providerID
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		m.registerBuiltModelsLocked([]Built{model})
	}
	if anchor >= 0 && anchor < prefix {
		added := append([]string(nil), m.modelOrder[prefix:]...)
		rest := append([]string(nil), m.modelOrder[anchor:prefix]...)
		m.modelOrder = append(append(m.modelOrder[:anchor], added...), rest...)
	}
	m.providerRevisions[providerID]++
	m.providerEpoch++
}

// ProviderCatalogRevision returns the revision for a saved provider catalog.
// It increases after each successful complete replacement or deletion.
func (m *Manager) ProviderCatalogRevision(providerID string) uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerRevisions[providerID]
}

// GetService returns the LLM service for modelID, wrapped with logging.
func (m *Manager) GetService(modelID string) (llm.Service, error) {
	service, _, _, err := m.GetServiceSnapshot(modelID)
	return service, err
}

// GetServiceSnapshot samples the route and its service configuration revision together.
func (m *Manager) GetServiceSnapshot(modelID string) (llm.Service, string, uint64, error) {
	entry, revision, err := m.lookup(modelID)
	if err != nil {
		return nil, "", 0, err
	}
	if entry.incompatible {
		return nil, "", 0, fmt.Errorf("model %s is known not to support text and tool calling", modelID)
	}
	if m.logger != nil {
		return &loggingService{
			service:  entry.service,
			logger:   m.logger,
			modelID:  entry.modelID,
			provider: entry.provider,
		}, entry.sourceID, revision, nil
	}
	return entry.service, entry.sourceID, revision, nil
}

// KnownAgentIncompatible excludes only authoritative non-text/non-tool routes.
func KnownAgentIncompatible(metadata *modelsdev.Metadata) bool {
	if metadata == nil {
		return false
	}
	if metadata.Authoritative("tool_call") && metadata.ToolCall != nil && !*metadata.ToolCall {
		return true
	}
	for field, modalities := range map[string][]string{
		"input_modalities": metadata.InputModalities, "output_modalities": metadata.OutputModalities,
	} {
		if metadata.Authoritative(field) && modalities != nil && !slices.Contains(modalities, "text") {
			return true
		}
	}
	return false
}

func (m *Manager) GetAvailableModels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.modelOrder))
	copy(result, m.modelOrder)
	return result
}

// GetTranscriptionModels returns known routes for an exact wire model name.
func (m *Manager) GetTranscriptionModels(modelName string) ([]TranscriptionModel, error) {
	m.mu.RLock()
	var result []TranscriptionModel
	for _, model := range m.transcriptionModels {
		if model.Model == modelName {
			result = append(result, model)
		}
	}
	m.mu.RUnlock()

	dbModels, err := m.customModelRows()
	if err != nil {
		return nil, err
	}
	for _, model := range dbModels {
		if model.ModelName != modelName || (model.ProviderType != "openai" && model.ProviderType != "openai-responses") {
			continue
		}
		result = append(result, TranscriptionModel{
			Model:    model.ModelName,
			Endpoint: strings.TrimSuffix(model.Endpoint, "/") + "/audio/transcriptions",
			APIKey:   model.ApiKey,
			Source:   SourceCustomLabel,
		})
	}
	return result, nil
}

func (m *Manager) HasModel(modelID string) bool {
	_, _, err := m.lookup(modelID)
	return err == nil
}

// SetProviderResolver supplies saved provider routes the catalog does not
// list, such as an existing conversation's model after discovery failed or
// the provider stopped listing it.
func (m *Manager) SetProviderResolver(resolve func(modelID string) (Built, error)) {
	m.mu.Lock()
	m.resolveProvider = resolve
	m.mu.Unlock()
}

// lookup returns a registered entry, or one built by the provider resolver.
// A resolved entry is not registered: it never appears in model lists. Its
// revision is returned only when no provider changed during resolution, so a
// concurrent rotation can never label an old service as current.
func (m *Manager) lookup(modelID string) (serviceEntry, uint64, error) {
	for {
		m.mu.RLock()
		entry, ok := m.services[modelID]
		revision := entry.serviceRevision
		epoch, resolve := m.providerEpoch, m.resolveProvider
		m.mu.RUnlock()
		if ok {
			return entry, revision, nil
		}
		if resolve == nil || !strings.HasPrefix(modelID, "provider:") {
			return serviceEntry{}, 0, fmt.Errorf("unsupported model: %s", modelID)
		}
		built, err := resolve(modelID)
		if err != nil {
			return serviceEntry{}, 0, err
		}
		m.mu.RLock()
		stable := m.providerEpoch == epoch
		revision = built.serviceRevision
		m.mu.RUnlock()
		if stable {
			return newServiceEntry(built), revision, nil
		}
	}
}

// ModelInfo contains display name, provider metadata, and release date for a model.
type ModelInfo struct {
	DisplayName string
	Provider    Provider
	Mode        string
	Source      string
	ReleaseDate string
	BaseURL     string
	APIType     string
	// APIModelName is the wire model name (e.g. "claude-opus-5"), for
	// models.dev lookups.
	APIModelName string
	SourceID     string
	Capabilities *modelsdev.Metadata
}

func (m *Manager) GetModelInfo(modelID string) *ModelInfo {
	entry, _, err := m.lookup(modelID)
	if err != nil {
		return nil
	}
	return &ModelInfo{
		DisplayName: entry.displayName, Provider: entry.provider, Mode: entry.mode,
		Source: entry.source, ReleaseDate: entry.releaseDate,
		BaseURL: entry.baseURL, APIType: string(entry.apiType),
		APIModelName: entry.apiModelName, SourceID: entry.sourceID,
		Capabilities: cloneProviderMetadata(entry.capabilities),
	}
}

type reasoningMapping struct {
	level  llm.ThinkingLevel
	effort string
}

type reasoningService struct {
	llm.Service
	supported     bool
	levels        []llm.ThinkingLevel
	mapping       map[llm.ThinkingLevel]reasoningMapping
	defaultSource llm.ThinkingLevel
}

func (s *reasoningService) SupportsReasoning() bool { return s.supported }
func (s *reasoningService) PatchProfile() string    { return llm.PatchProfile(s.Service) }
func (s *reasoningService) SupportsServerSideWebSearch() bool {
	type capable interface{ SupportsServerSideWebSearch() bool }
	c, ok := s.Service.(capable)
	return ok && c.SupportsServerSideWebSearch()
}

func (s *reasoningService) SupportedReasoningLevels() []llm.ThinkingLevel {
	if len(s.levels) == 0 {
		return llm.SupportedReasoningLevels(s.Service)
	}
	return append([]llm.ThinkingLevel(nil), s.levels...)
}

func (s *reasoningService) DefaultReasoningLevel() string {
	if !s.supported {
		return "off"
	}
	if mapped, ok := s.mapping[s.defaultSource]; ok {
		if mapped.level != llm.ThinkingLevelDefault {
			return mapped.level.Name()
		}
		return mapped.effort
	}
	return llm.ServiceDefaultReasoningLevel(s.Service)
}

func (s *reasoningService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	mapped, err := s.mapRequest(req)
	if err != nil {
		return nil, err
	}
	return s.Service.Do(ctx, mapped)
}

func (s *reasoningService) CacheTTL() time.Duration { return llm.CacheTTL(s.Service) }

// WarmCache maps req as Do does: reasoning effort is part of the cache key.
func (s *reasoningService) WarmCache(ctx context.Context, req *llm.Request) (llm.Usage, error) {
	mapped, err := s.mapRequest(req)
	if err != nil {
		return llm.Usage{}, err
	}
	return llm.WarmCache(ctx, s.Service, mapped)
}

func (s *reasoningService) mapRequest(req *llm.Request) (*llm.Request, error) {
	copyReq := *req
	if !s.supported {
		copyReq.ThinkingLevel = llm.ThinkingLevelOff
	} else if copyReq.ThinkingLevel == llm.ThinkingLevelDefault {
		if mapped, ok := s.mapping[s.defaultSource]; ok {
			copyReq.ThinkingLevel = mapped.level
			copyReq.ReasoningEffort = mapped.effort
		}
	} else if len(s.mapping) > 0 {
		mapped, ok := s.mapping[copyReq.ThinkingLevel]
		if !ok {
			return nil, fmt.Errorf("reasoning level %q is not supported by this model", copyReq.ThinkingLevel.Name())
		}
		copyReq.ThinkingLevel = mapped.level
		copyReq.ReasoningEffort = mapped.effort
	}
	return &copyReq, nil
}

func wrapReasoningService(service llm.Service, model *generated.Model) llm.Service {
	return WrapReasoningConfig(service, model.Endpoint, model.ModelName, model.ReasoningSupport, model.ReasoningMap)
}

// WrapReasoningConfig applies custom-model capability and level mapping.
func WrapReasoningConfig(service llm.Service, endpoint, modelName, support, rawMap string) llm.Service {
	supported := ResolveSupportsReasoning(endpoint, modelName, support)
	mapping, levels := parseReasoningMap(rawMap)
	defaultSource := llm.ParseThinkingLevel(llm.ServiceDefaultReasoningLevel(service))
	return &reasoningService{Service: service, supported: supported, levels: levels, mapping: mapping, defaultSource: defaultSource}
}

func parseReasoningMap(raw string) (map[llm.ThinkingLevel]reasoningMapping, []llm.ThinkingLevel) {
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, nil
	}
	mapping := make(map[llm.ThinkingLevel]reasoningMapping, len(values))
	levels := make([]llm.ThinkingLevel, 0, len(values))
	for _, level := range []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelMinimal, llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax} {
		mappedName, ok := values[level.Name()]
		if !ok || mappedName == "" {
			continue
		}
		mapped := llm.ParseThinkingLevel(mappedName)
		if mapped == llm.ThinkingLevelDefault && mappedName != "default" {
			// Preserve the generic level as a safe fallback for providers that do
			// not consume verbatim efforts; providers that do consume it use effort.
			mapping[level] = reasoningMapping{level: level, effort: mappedName}
		} else {
			mapping[level] = reasoningMapping{level: mapped}
		}
		levels = append(levels, level)
	}
	return mapping, levels
}

// createServiceFromModel creates an LLM service from a database model configuration.
func (m *Manager) createServiceFromModel(model *generated.Model) llm.Service {
	supportsImages := ResolveSupportsImages(model.Endpoint, model.ModelName, model.ImageSupport)
	var service llm.Service
	switch model.ProviderType {
	case "anthropic":
		service = &ant.Service{
			APIKey:          model.ApiKey,
			URL:             model.Endpoint,
			Model:           model.ModelName,
			MaxTokens:       int(model.MaxTokens),
			HTTPC:           m.httpc,
			ThinkingLevel:   llm.ThinkingLevelMedium,
			SupportsImages_: supportsImages,
		}
	case "openai":
		service = &oai.Service{
			APIKey:    model.ApiKey,
			ModelURL:  model.Endpoint,
			MaxTokens: int(model.MaxTokens),
			Model: oai.Model{
				UserName:         "",
				ModelName:        model.ModelName,
				TextVerbosity:    "",
				URL:              model.Endpoint,
				APIKeyEnv:        "",
				IsReasoningModel: false,
				SupportsImages:   supportsImages,
			},
			HTTPC:           m.httpc,
			ProviderName:    "openai",
			ReasoningEffort: model.ReasoningEffort,
			ReasoningReplay: oai.ReasoningReplay(model.ReasoningReplay),
		}
	case "openai-responses":
		service = &oai.ResponsesService{
			APIKey:    model.ApiKey,
			ModelURL:  model.Endpoint,
			MaxTokens: int(model.MaxTokens),
			Model: oai.Model{
				UserName:         "",
				ModelName:        model.ModelName,
				TextVerbosity:    "",
				URL:              model.Endpoint,
				APIKeyEnv:        "",
				IsReasoningModel: false,
				SupportsImages:   supportsImages,
			},
			HTTPC:           m.httpc,
			ThinkingLevel:   llm.ThinkingLevelMedium,
			ReasoningEffort: model.ReasoningEffort,
			ProviderName:    "openai",
			ReasoningReplay: oai.ReasoningReplay(model.ReasoningReplay),
		}
	case "gemini":
		service = &gem.Service{
			APIKey:          model.ApiKey,
			URL:             model.Endpoint,
			Model:           model.ModelName,
			MaxTokens:       int(model.MaxTokens),
			HTTPC:           m.httpc,
			ReasoningEffort: model.ReasoningEffort,
			SupportsImages_: supportsImages,
		}
	default:
		if m.logger != nil {
			m.logger.Error("Unknown provider type for model", "model_id", model.ModelID, "provider_type", model.ProviderType)
		}
		return nil
	}
	return wrapReasoningService(service, model)
}

// ResolveSupportsImages turns a stored image_support value ("auto"|"yes"|"no")
// into a SupportsImages bool. "auto" is resolved from the model's endpoint URL
// and name; unknown models default to allowing images.
func ResolveSupportsImages(endpoint, modelName, imageSupport string) bool {
	switch imageSupport {
	case "yes":
		return true
	case "no":
		return false
	case "auto", "":
		supported, found := modelsdev.LookupImageSupport(endpoint, modelName)
		if !found {
			return true
		}
		return supported
	default:
		return true
	}
}

// ResolveSupportsReasoning turns a stored reasoning_support value
// ("auto"|"yes"|"no") into a capability boolean. Unknown models default to
// supporting reasoning so custom endpoints remain configurable.
func ResolveSupportsReasoning(endpoint, modelName, reasoningSupport string) bool {
	switch reasoningSupport {
	case "yes":
		return true
	case "no":
		return false
	case "auto", "":
		supported, found := modelsdev.LookupReasoningSupport(endpoint, modelName)
		if !found {
			return true
		}
		return supported
	default:
		return true
	}
}
