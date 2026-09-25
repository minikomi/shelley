package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/transcription"
)

// openAITranscriptionProvider implements the selectable-model contract. The
// transcript operation is separate from timecodes, which receive no context.
type openAITranscriptionProvider struct {
	client   *http.Client
	mediaRun mediaCommandRunner
}

func newOpenAITranscriptionProvider(client *http.Client, mediaRun mediaCommandRunner) *openAITranscriptionProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &openAITranscriptionProvider{client: client, mediaRun: mediaRun}
}

func (*openAITranscriptionProvider) Protocol() transcription.Protocol {
	return transcription.ProtocolOpenAI
}

func (p *openAITranscriptionProvider) Transcribe(ctx context.Context, request transcription.Request) (transcription.Result, error) {
	if request.Model.Protocol != transcription.ProtocolOpenAI {
		return transcription.Result{}, fmt.Errorf("OpenAI provider cannot transcribe protocol %q", request.Model.Protocol)
	}
	modelName := strings.TrimSpace(request.Model.Model)
	if modelName == "" {
		return transcription.Result{}, errors.New("OpenAI transcription requires a selected model")
	}

	profile, options, err := openAITranscriptionOptions(request.Model.APIProfile, modelName, request.Role)
	if err != nil {
		return transcription.Result{}, err
	}
	encoding, err := openAITranscriptionRequestEncoding(request.Model.RequestEncoding, modelName)
	if err != nil {
		return transcription.Result{}, err
	}
	if !request.Model.Supports(request.Role) {
		return transcription.Result{}, fmt.Errorf("model %q does not support %s transcription", request.Model.ID, request.Role)
	}
	includePrompt := request.Role == transcription.RoleTranscript && request.Model.SupportsPrompted
	if encoding == transcription.RequestEncodingBase64JSON && includePrompt {
		return transcription.Result{}, errors.New("base64 JSON transcription does not support sending prompts; select a transcription model with prompt support")
	}

	var uploadPath string
	var cleanup func()
	if encoding == transcription.RequestEncodingBase64JSON {
		uploadPath, cleanup, err = prepareBase64TranscriptionUpload(ctx, request.RecordingPath, p.mediaRun)
	} else {
		uploadPath, cleanup, err = prepareTranscriptionUpload(ctx, request.RecordingPath, p.mediaRun)
	}
	if err != nil {
		return transcription.Result{}, err
	}
	defer cleanup()
	body, contentType, err := openAITranscriptionBody(uploadPath, request.Prompt, modelName, options, includePrompt, encoding)
	if err != nil {
		return transcription.Result{}, err
	}
	response, err := p.request(ctx, request.Model, contentType, body, request.Role == transcription.RoleTranscript)
	if err != nil {
		return transcription.Result{}, err
	}
	if request.Role == transcription.RoleTimecoded {
		if err := validateOpenAITimecodedResponse(response.Body, profile, encoding); err != nil {
			return transcription.Result{}, err
		}
		return transcription.Result{Transcript: response.Text, Timecodes: response.Body}, nil
	}
	return transcription.Result{Transcript: response.Text}, nil
}

func prepareBase64TranscriptionUpload(ctx context.Context, mediaPath string, mediaRun mediaCommandRunner) (string, func(), error) {
	if strings.EqualFold(filepath.Ext(mediaPath), ".wav") {
		return mediaPath, func() {}, nil
	}
	if mediaRun == nil {
		return "", func() {}, errors.New("base64 JSON transcription preparation requires a media command runner")
	}
	file, err := os.CreateTemp("", "shelley-transcription-*.wav")
	if err != nil {
		return "", func() {}, fmt.Errorf("create WAV transcription audio: %w", err)
	}
	preparedPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(preparedPath)
		return "", func() {}, fmt.Errorf("close WAV transcription audio: %w", err)
	}
	cleanup := func() { _ = os.Remove(preparedPath) }
	if _, err := mediaRun(ctx, "ffmpeg", "-y", "-i", mediaPath, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", preparedPath); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("prepare WAV transcription audio: %w", err)
	}
	info, err := os.Stat(preparedPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("inspect WAV transcription audio: %w", err)
	}
	if info.Size() > maxTranscriptionUpload {
		cleanup()
		return "", func() {}, errors.New("prepared WAV recording is larger than 25 MB")
	}
	return preparedPath, cleanup, nil
}

func openAITranscriptionRequestEncoding(configured transcription.RequestEncoding, model string) (transcription.RequestEncoding, error) {
	encoding, err := transcription.NormalizeRequestEncoding(configured, transcription.ProtocolOpenAI)
	if err != nil {
		return "", err
	}
	if encoding != transcription.RequestEncodingAuto {
		return encoding, nil
	}
	return transcription.InferOpenAIRequestEncoding(model), nil
}

// openAITranscriptionOptions selects the wire format after resolving an
// explicit profile or the conservative compatibility behavior for auto.
func openAITranscriptionOptions(configured transcription.APIProfile, model string, role transcription.Role) (transcription.APIProfile, transcriptionAPIOptions, error) {
	profile, err := transcription.NormalizeAPIProfile(configured, transcription.ProtocolOpenAI)
	if err != nil {
		return "", transcriptionAPIOptions{}, err
	}
	if profile == transcription.APIProfileAuto {
		profile = transcription.InferOpenAIProfile(model)
		if profile == transcription.APIProfileAuto {
			switch role {
			case transcription.RoleTranscript:
				return profile, textTranscription, nil
			case transcription.RoleTimecoded:
				return transcription.APIProfileOpenAIWhisper, timestampTranscription, nil
			default:
				return "", transcriptionAPIOptions{}, fmt.Errorf("OpenAI provider does not support transcription role %q", role)
			}
		}
	}

	switch profile {
	case transcription.APIProfileOpenAIJSON:
		if role != transcription.RoleTranscript {
			return "", transcriptionAPIOptions{}, fmt.Errorf("OpenAI API profile %q is transcript-only; select openai-whisper or openai-diarized for timecoded transcription", profile)
		}
		return profile, textTranscription, nil
	case transcription.APIProfileOpenAIWhisper:
		switch role {
		case transcription.RoleTranscript:
			return profile, whisperTextTranscription, nil
		case transcription.RoleTimecoded:
			return profile, timestampTranscription, nil
		default:
			return "", transcriptionAPIOptions{}, fmt.Errorf("OpenAI provider does not support transcription role %q", role)
		}
	case transcription.APIProfileOpenAIDiarized:
		if role != transcription.RoleTranscript && role != transcription.RoleTimecoded {
			return "", transcriptionAPIOptions{}, fmt.Errorf("OpenAI provider does not support transcription role %q", role)
		}
		return profile, diarizedTimestampTranscription, nil
	default:
		return "", transcriptionAPIOptions{}, fmt.Errorf("OpenAI API profile %q is not supported", profile)
	}
}

func validateOpenAITimecodedResponse(body []byte, profile transcription.APIProfile, encoding transcription.RequestEncoding) error {
	if profile == transcription.APIProfileOpenAIDiarized {
		return validateDiarizedTimestampResponse(body)
	}
	if encoding == transcription.RequestEncodingBase64JSON {
		return validateSegmentTimestampResponse(body)
	}
	return validateTimestampResponse(body)
}

func openAITranscriptionBody(mediaPath, prompt, model string, options transcriptionAPIOptions, includePrompt bool, encoding transcription.RequestEncoding) (io.ReadCloser, string, error) {
	if encoding == transcription.RequestEncodingBase64JSON {
		return openAIBase64TranscriptionBody(mediaPath, model, options.ResponseFormat)
	}
	media, err := os.Open(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("open recording: %w", err)
	}
	reader, pipe := io.Pipe()
	writer := multipart.NewWriter(pipe)
	contentType := writer.FormDataContentType()
	go func() {
		defer media.Close()
		writeErr := func() error {
			if err := writer.WriteField("model", model); err != nil {
				return err
			}
			if err := writer.WriteField("response_format", options.ResponseFormat); err != nil {
				return err
			}
			if options.ChunkingStrategy != "" {
				if err := writer.WriteField("chunking_strategy", options.ChunkingStrategy); err != nil {
					return err
				}
			}
			for _, granularity := range options.TimestampGranularities {
				if err := writer.WriteField("timestamp_granularities[]", granularity); err != nil {
					return err
				}
			}
			if includePrompt && prompt != "" {
				if err := writer.WriteField("prompt", prompt); err != nil {
					return err
				}
			}
			part, err := writer.CreateFormFile("file", filepath.Base(mediaPath))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, media); err != nil {
				return fmt.Errorf("read recording: %w", err)
			}
			return writer.Close()
		}()
		if writeErr != nil {
			_ = pipe.CloseWithError(writeErr)
			return
		}
		_ = pipe.Close()
	}()
	return reader, contentType, nil
}

func openAIBase64TranscriptionBody(mediaPath, model, responseFormat string) (io.ReadCloser, string, error) {
	media, err := os.Open(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("open recording: %w", err)
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(mediaPath)), ".")
	if format == "" {
		media.Close()
		return nil, "", errors.New("base64 JSON transcription requires a recording file extension")
	}
	modelJSON, err := json.Marshal(model)
	if err != nil {
		media.Close()
		return nil, "", err
	}
	formatJSON, err := json.Marshal(format)
	if err != nil {
		media.Close()
		return nil, "", err
	}
	responseFormatJSON, err := json.Marshal(responseFormat)
	if err != nil {
		media.Close()
		return nil, "", err
	}
	reader, pipe := io.Pipe()
	go func() {
		defer media.Close()
		if _, err := fmt.Fprintf(pipe, `{"model":%s,"input_audio":{"data":"`, modelJSON); err != nil {
			_ = pipe.CloseWithError(err)
			return
		}
		encoder := base64.NewEncoder(base64.StdEncoding, pipe)
		if _, err := io.Copy(encoder, media); err != nil {
			_ = encoder.Close()
			_ = pipe.CloseWithError(fmt.Errorf("read recording: %w", err))
			return
		}
		if err := encoder.Close(); err != nil {
			_ = pipe.CloseWithError(err)
			return
		}
		if _, err := fmt.Fprintf(pipe, `","format":%s}`, formatJSON); err != nil {
			_ = pipe.CloseWithError(err)
			return
		}
		if responseFormat != "" && responseFormat != "json" {
			if _, err := fmt.Fprintf(pipe, `,"response_format":%s`, responseFormatJSON); err != nil {
				_ = pipe.CloseWithError(err)
				return
			}
		}
		if _, err := io.WriteString(pipe, `}`); err != nil {
			_ = pipe.CloseWithError(err)
			return
		}
		_ = pipe.Close()
	}()
	return reader, "application/json", nil
}

func (p *openAITranscriptionProvider) request(ctx context.Context, model transcription.Model, contentType string, body io.ReadCloser, requireText bool) (transcriptionAPIResponse, error) {
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, model.Endpoint, body)
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	if model.APIKey != "" && model.APIKey != "implicit" {
		req.Header.Set("Authorization", "Bearer "+model.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("OpenAI transcription request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := readBoundedTranscriptionResponse(resp.Body, "OpenAI")
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return transcriptionAPIResponse{}, openAITranscriptionHTTPError(resp, responseBody)
	}

	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("decode OpenAI transcription response: %w", err)
	}
	decoded.Text = strings.TrimSpace(decoded.Text)
	if requireText && decoded.Text == "" {
		return transcriptionAPIResponse{}, errors.New("OpenAI transcription returned an empty transcript")
	}
	return transcriptionAPIResponse{Text: decoded.Text, Body: responseBody}, nil
}

func readBoundedTranscriptionResponse(body io.Reader, provider string) ([]byte, error) {
	responseBody, err := io.ReadAll(io.LimitReader(body, maxTranscriptionResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read %s transcription response: %w", provider, err)
	}
	if len(responseBody) > maxTranscriptionResponse {
		return nil, fmt.Errorf("%s transcription response exceeded 16 MiB", provider)
	}
	return responseBody, nil
}

func openAITranscriptionHTTPError(resp *http.Response, responseBody []byte) error {
	errorBody := responseBody
	if len(errorBody) > maxTranscriptionErrorBody {
		errorBody = errorBody[:maxTranscriptionErrorBody]
	}
	message := strings.TrimSpace(string(errorBody))
	var apiError struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(errorBody, &apiError) == nil && apiError.Error.Message != "" {
		message = apiError.Error.Message
	}
	if message == "" {
		message = resp.Status
	}
	return &transcriptionHTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Message: message}
}

// deepgramTranscriptionProvider calls Deepgram's native pre-recorded REST API.
// Request.Prompt is deliberately not converted to a Deepgram query parameter:
// Deepgram documents keyterm/keyword lists, not arbitrary prompt text, and the
// provider-neutral request has no keyterm list to safely send.
type deepgramTranscriptionProvider struct {
	client *http.Client
}

func newDeepgramTranscriptionProvider(client *http.Client) *deepgramTranscriptionProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &deepgramTranscriptionProvider{client: client}
}

func (*deepgramTranscriptionProvider) Protocol() transcription.Protocol {
	return transcription.ProtocolDeepgram
}

func (p *deepgramTranscriptionProvider) Transcribe(ctx context.Context, request transcription.Request) (transcription.Result, error) {
	if request.Model.Protocol != transcription.ProtocolDeepgram {
		return transcription.Result{}, fmt.Errorf("Deepgram provider cannot transcribe protocol %q", request.Model.Protocol)
	}
	if request.Model.SupportsPrompted {
		return transcription.Result{}, errors.New("Deepgram model configuration must not advertise free-form context prompts")
	}
	if !request.Model.Supports(request.Role) {
		return transcription.Result{}, fmt.Errorf("model %q does not support %s transcription", request.Model.ID, request.Role)
	}
	if strings.TrimSpace(request.Model.Model) == "" {
		return transcription.Result{}, errors.New("Deepgram transcription requires a selected model")
	}
	if !request.Model.Managed && (strings.TrimSpace(request.Model.APIKey) == "" || request.Model.APIKey == "implicit") {
		return transcription.Result{}, errors.New("custom Deepgram prerecorded transcription requires an API key for Token authorization")
	}
	contentType, err := deepgramRecordingContentType(request.RecordingPath)
	if err != nil {
		return transcription.Result{}, err
	}
	media, err := os.Open(request.RecordingPath)
	if err != nil {
		return transcription.Result{}, fmt.Errorf("open recording: %w", err)
	}
	defer media.Close()
	mediaInfo, err := media.Stat()
	if err != nil {
		return transcription.Result{}, fmt.Errorf("inspect recording: %w", err)
	}
	if !mediaInfo.Mode().IsRegular() {
		return transcription.Result{}, errors.New("recording must be a regular file")
	}

	endpoint, err := url.Parse(request.Model.Endpoint)
	if err != nil {
		return transcription.Result{}, fmt.Errorf("parse Deepgram endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("model", request.Model.Model)
	query.Set("mip_opt_out", "true")
	query.Set("punctuate", "true")
	if request.Role == transcription.RoleTimecoded {
		query.Set("utterances", "true")
	}
	endpoint.RawQuery = query.Encode()

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), media)
	if err != nil {
		return transcription.Result{}, err
	}
	if request.Model.APIKey != "" && request.Model.APIKey != "implicit" {
		httpRequest.Header.Set("Authorization", "Token "+request.Model.APIKey)
	}
	httpRequest.Header.Set("Content-Type", contentType)
	httpRequest.ContentLength = mediaInfo.Size()
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return transcription.Result{}, fmt.Errorf("Deepgram transcription request: %w", err)
	}
	defer response.Body.Close()
	body, err := readBoundedTranscriptionResponse(response.Body, "Deepgram")
	if err != nil {
		return transcription.Result{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return transcription.Result{}, deepgramTranscriptionHTTPError(response, body)
	}

	decoded, err := decodeDeepgramTranscription(body)
	if err != nil {
		return transcription.Result{}, err
	}
	if request.Role == transcription.RoleTranscript {
		return transcription.Result{Transcript: decoded.transcript}, nil
	}
	timecodes, err := decoded.neutralTimecodes()
	if err != nil {
		return transcription.Result{}, err
	}
	return transcription.Result{Transcript: decoded.transcript, Timecodes: timecodes}, nil
}

func deepgramRecordingContentType(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".aac":
		return "audio/aac", nil
	case ".flac":
		return "audio/flac", nil
	case ".m4a":
		return "audio/mp4", nil
	case ".mp3", ".mpga", ".mpeg":
		return "audio/mpeg", nil
	case ".mp4":
		return "video/mp4", nil
	case ".ogg", ".opus":
		return "audio/ogg", nil
	case ".wav":
		return "audio/wav", nil
	case ".webm":
		return "video/webm", nil
	}
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" && contentType != "application/octet-stream" {
		return contentType, nil
	}
	return "", fmt.Errorf("unsupported recording content type for %q", filepath.Ext(path))
}

func deepgramTranscriptionHTTPError(resp *http.Response, body []byte) error {
	errorBody := body
	if len(errorBody) > maxTranscriptionErrorBody {
		errorBody = errorBody[:maxTranscriptionErrorBody]
	}
	message := strings.TrimSpace(string(errorBody))
	var decoded struct {
		ErrMsg  string `json:"err_msg"`
		Message string `json:"message"`
	}
	if json.Unmarshal(errorBody, &decoded) == nil {
		if decoded.ErrMsg != "" {
			message = decoded.ErrMsg
		} else if decoded.Message != "" {
			message = decoded.Message
		}
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("Deepgram transcription failed (%s): %s", resp.Status, message)
}

type deepgramResponse struct {
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
				Words      []struct {
					Word  string  `json:"word"`
					Start float64 `json:"start"`
					End   float64 `json:"end"`
				} `json:"words"`
			} `json:"alternatives"`
		} `json:"channels"`
		Utterances []struct {
			Transcript string  `json:"transcript"`
			Start      float64 `json:"start"`
			End        float64 `json:"end"`
		} `json:"utterances"`
	} `json:"results"`
}

type decodedDeepgramTranscription struct {
	transcript string
	words      []neutralTranscriptionWord
	utterances []neutralTranscriptionSegment
	wordsSet   bool
	uttsSet    bool
}

type neutralTranscriptionWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type neutralTranscriptionSegment struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type neutralTranscriptionTimecodes struct {
	Text     string                        `json:"text"`
	Words    []neutralTranscriptionWord    `json:"words"`
	Segments []neutralTranscriptionSegment `json:"segments"`
}

func decodeDeepgramTranscription(body []byte) (decodedDeepgramTranscription, error) {
	var response deepgramResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return decodedDeepgramTranscription{}, fmt.Errorf("decode Deepgram transcription response: %w", err)
	}
	if len(response.Results.Channels) == 0 || len(response.Results.Channels[0].Alternatives) == 0 {
		return decodedDeepgramTranscription{}, errors.New("Deepgram transcription response is missing results.channels[0].alternatives[0]")
	}
	alternative := response.Results.Channels[0].Alternatives[0]
	decoded := decodedDeepgramTranscription{
		transcript: strings.TrimSpace(alternative.Transcript),
		wordsSet:   alternative.Words != nil,
		uttsSet:    response.Results.Utterances != nil,
		words:      make([]neutralTranscriptionWord, 0, len(alternative.Words)),
		utterances: make([]neutralTranscriptionSegment, 0, len(response.Results.Utterances)),
	}
	for _, word := range alternative.Words {
		word.Word = strings.TrimSpace(word.Word)
		if word.Word == "" || !validTranscriptionRange(word.Start, word.End) {
			return decodedDeepgramTranscription{}, errors.New("Deepgram transcription response contains an invalid word timestamp")
		}
		decoded.words = append(decoded.words, neutralTranscriptionWord{Word: word.Word, Start: word.Start, End: word.End})
	}
	for _, utterance := range response.Results.Utterances {
		utterance.Transcript = strings.TrimSpace(utterance.Transcript)
		if utterance.Transcript == "" || !validTranscriptionRange(utterance.Start, utterance.End) {
			return decodedDeepgramTranscription{}, errors.New("Deepgram transcription response contains an invalid utterance timestamp")
		}
		decoded.utterances = append(decoded.utterances, neutralTranscriptionSegment{Text: utterance.Transcript, Start: utterance.Start, End: utterance.End})
	}
	return decoded, nil
}

func validTranscriptionRange(start, end float64) bool {
	return start >= 0 && end >= start && !math.IsNaN(start) && !math.IsInf(start, 0) && !math.IsNaN(end) && !math.IsInf(end, 0)
}

func (d decodedDeepgramTranscription) neutralTimecodes() (json.RawMessage, error) {
	if !d.wordsSet {
		return nil, errors.New("Deepgram transcription response is missing word timestamps")
	}
	if !d.uttsSet {
		return nil, errors.New("Deepgram transcription response is missing utterance timestamps")
	}
	body, err := json.Marshal(neutralTranscriptionTimecodes{Text: d.transcript, Words: d.words, Segments: d.utterances})
	if err != nil {
		return nil, fmt.Errorf("encode Deepgram timestamp response: %w", err)
	}
	return body, nil
}

// RegisterBuiltinTranscriptionProviders installs the native providers used by
// configured selectable transcription models. It is called by the production
// entrypoint; tests may register focused provider doubles instead.
func (s *Server) RegisterBuiltinTranscriptionProviders() error {
	for _, provider := range []transcription.Provider{
		newOpenAITranscriptionProvider(nil, s.mediaRun),
		newDeepgramTranscriptionProvider(nil),
	} {
		if err := s.RegisterTranscriptionProvider(provider); err != nil {
			return err
		}
	}
	return nil
}
