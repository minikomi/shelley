package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

const (
	openAITranscriptionModel            = "gpt-transcribe"
	openAITimestampedTranscriptionModel = "whisper-1"
	maxTranscriptionUpload              = 25_000_000
	maxTranscriptionErrorBody           = 64 << 10
	maxTranscriptionResponse            = 16 << 20
	transcriptionRoutingRejection       = "ChatGPT subscriptions do not support transcription; use an LLM integration with managed OpenAI or BYOK"
	transcriptionIntegrationMissing     = "integration not found or not attached to this VM (trace: "
)

type transcriptionResult struct {
	Text            string
	Model           string
	TimestampsModel string
	TimestampsPath  string
}

type transcriptionAPIOptions struct {
	Model                  string
	ResponseFormat         string
	TimestampGranularities []string
}

var (
	textTranscription = transcriptionAPIOptions{
		Model:          openAITranscriptionModel,
		ResponseFormat: "json",
	}
	timestampTranscription = transcriptionAPIOptions{
		Model:                  openAITimestampedTranscriptionModel,
		ResponseFormat:         "verbose_json",
		TimestampGranularities: []string{"word", "segment"},
	}
)

type recordingTranscriber interface {
	Transcribe(context.Context, string, string, bool) (transcriptionResult, error)
}

// transcriptionEndpoints are tried in fixed order.
var transcriptionEndpoints = []string{
	"https://llm.int.exe.xyz/v1/audio/transcriptions",
	"https://openai.int.exe.xyz/v1/audio/transcriptions",
}

type openAIRecordingTranscriber struct {
	client    *http.Client
	endpoints []string
	mediaRun  mediaCommandRunner
}

type transcriptionAPIResponse struct {
	Text string
	Body []byte
}

type transcriptionHTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

type transcriptionModelError struct {
	Model string
	Err   error
}

func (e *transcriptionModelError) Error() string { return e.Err.Error() }
func (e *transcriptionModelError) Unwrap() error { return e.Err }

func withTranscriptionModel(model string, err error) error {
	if err == nil {
		return nil
	}
	return &transcriptionModelError{Model: model, Err: err}
}

func (e *transcriptionHTTPError) Error() string {
	return fmt.Sprintf("OpenAI transcription failed (%s): %s", e.Status, e.Message)
}

func newOpenAIRecordingTranscriber() recordingTranscriber {
	return &openAIRecordingTranscriber{
		client:    http.DefaultClient,
		endpoints: transcriptionEndpoints,
		mediaRun:  runMediaCommand,
	}
}

func (t *openAIRecordingTranscriber) Transcribe(ctx context.Context, mediaPath, prompt string, timestamps bool) (transcriptionResult, error) {
	uploadPath, cleanup, err := t.prepareUpload(ctx, mediaPath)
	if err != nil {
		return transcriptionResult{}, err
	}
	defer cleanup()

	if !timestamps {
		response, err := t.transcribe(ctx, uploadPath, prompt, textTranscription)
		if err != nil {
			return transcriptionResult{}, withTranscriptionModel(textTranscription.Model, err)
		}
		return transcriptionResult{Text: response.Text, Model: textTranscription.Model}, nil
	}

	var transcriptResponse, timestampResponse transcriptionAPIResponse
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		transcriptResponse, err = t.transcribe(groupContext, uploadPath, prompt, textTranscription)
		return withTranscriptionModel(textTranscription.Model, err)
	})
	group.Go(func() error {
		var err error
		timestampResponse, err = t.transcribe(groupContext, uploadPath, prompt, timestampTranscription)
		return withTranscriptionModel(timestampTranscription.Model, err)
	})
	if err := group.Wait(); err != nil {
		return transcriptionResult{}, err
	}
	if err := validateTimestampResponse(timestampResponse.Body); err != nil {
		return transcriptionResult{}, withTranscriptionModel(timestampTranscription.Model, err)
	}

	timestampsPath := mediaPath + ".timestamps.json"
	if err := os.WriteFile(timestampsPath, timestampResponse.Body, 0o600); err != nil {
		return transcriptionResult{}, fmt.Errorf("write transcription timestamps: %w", err)
	}
	return transcriptionResult{
		Text:            transcriptResponse.Text,
		Model:           textTranscription.Model,
		TimestampsModel: timestampTranscription.Model,
		TimestampsPath:  timestampsPath,
	}, nil
}

func (t *openAIRecordingTranscriber) prepareUpload(ctx context.Context, mediaPath string) (string, func(), error) {
	info, err := os.Stat(mediaPath)
	if err != nil {
		return "", func() {}, fmt.Errorf("inspect recording upload: %w", err)
	}
	if info.Size() <= maxTranscriptionUpload {
		return mediaPath, func() {}, nil
	}
	if t.mediaRun == nil {
		return "", func() {}, errors.New("oversized recording preparation requires a media command runner")
	}

	file, err := os.CreateTemp("", "shelley-transcription-*.m4a")
	if err != nil {
		return "", func() {}, fmt.Errorf("create prepared transcription audio: %w", err)
	}
	preparedPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(preparedPath)
		return "", func() {}, fmt.Errorf("close prepared transcription audio: %w", err)
	}
	cleanup := func() { _ = os.Remove(preparedPath) }
	if _, err := t.mediaRun(ctx, "ffmpeg", "-y", "-i", mediaPath, "-map", "0:a:0", "-vn", "-c:a", "aac", "-b:a", "64k", preparedPath); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("prepare oversized recording audio: %w", err)
	}
	preparedInfo, err := os.Stat(preparedPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("inspect prepared transcription audio: %w", err)
	}
	if preparedInfo.Size() > maxTranscriptionUpload {
		cleanup()
		return "", func() {}, errors.New("prepared recording is still larger than 25 MB; split it or use the transcribing-audio skill for chunked transcription")
	}
	return preparedPath, cleanup, nil
}

func validateTimestampResponse(body []byte) error {
	var decoded struct {
		Words    []json.RawMessage `json:"words"`
		Segments []json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode Whisper timestamp response: %w", err)
	}
	if decoded.Words == nil {
		return errors.New(`Whisper timestamp response field "words" must be an array`)
	}
	if decoded.Segments == nil {
		return errors.New(`Whisper timestamp response field "segments" must be an array`)
	}
	return nil
}

func (t *openAIRecordingTranscriber) transcribe(ctx context.Context, mediaPath, prompt string, options transcriptionAPIOptions) (transcriptionAPIResponse, error) {
	media, err := os.Open(mediaPath)
	if err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("open recording: %w", err)
	}
	defer media.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", options.Model); err != nil {
		return transcriptionAPIResponse{}, err
	}
	if err := writer.WriteField("response_format", options.ResponseFormat); err != nil {
		return transcriptionAPIResponse{}, err
	}
	for _, granularity := range options.TimestampGranularities {
		if err := writer.WriteField("timestamp_granularities[]", granularity); err != nil {
			return transcriptionAPIResponse{}, err
		}
	}
	if err := writer.WriteField("prompt", prompt); err != nil {
		return transcriptionAPIResponse{}, err
	}
	part, err := writer.CreateFormFile("file", filepath.Base(mediaPath))
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	if _, err := io.Copy(part, media); err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("read recording: %w", err)
	}
	if err := writer.Close(); err != nil {
		return transcriptionAPIResponse{}, err
	}

	contentType := writer.FormDataContentType()
	var response transcriptionAPIResponse
	for index, endpoint := range t.endpoints {
		response, err = t.request(ctx, endpoint, contentType, body.Bytes(), options.ResponseFormat != "verbose_json")
		if err == nil {
			break
		}
		if index == len(t.endpoints)-1 || !isTranscriptionRoutingError(err) {
			return transcriptionAPIResponse{}, err
		}
	}
	return response, err
}

func isTranscriptionRoutingError(err error) bool {
	var httpError *transcriptionHTTPError
	if !errors.As(err, &httpError) {
		return false
	}
	return (httpError.StatusCode == http.StatusBadRequest &&
		strings.HasPrefix(httpError.Message, transcriptionRoutingRejection)) ||
		(httpError.StatusCode == http.StatusForbidden &&
			strings.HasPrefix(httpError.Message, transcriptionIntegrationMissing))
}

func (t *openAIRecordingTranscriber) request(ctx context.Context, endpoint, contentType string, body []byte, requireText bool) (transcriptionAPIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return transcriptionAPIResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := t.client.Do(req)
	if err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("OpenAI transcription request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTranscriptionResponse+1))
	if err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("read OpenAI transcription response: %w", err)
	}
	if len(responseBody) > maxTranscriptionResponse {
		return transcriptionAPIResponse{}, errors.New("OpenAI transcription response exceeded 16 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
		return transcriptionAPIResponse{}, &transcriptionHTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Message:    message,
		}
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
