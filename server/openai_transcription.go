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
	openAITranscriptionModel            = "gpt-4o-transcribe"
	openAITimestampedTranscriptionModel = "whisper-1"
	maxTranscriptionErrorBody           = 64 << 10
	maxTranscriptionResponse            = 16 << 20
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

func directTranscriptionOptions(timestamps bool) transcriptionAPIOptions {
	if timestamps {
		return transcriptionAPIOptions{
			Model:                  openAITimestampedTranscriptionModel,
			ResponseFormat:         "verbose_json",
			TimestampGranularities: []string{"word", "segment"},
		}
	}
	return transcriptionAPIOptions{
		Model:          openAITranscriptionModel,
		ResponseFormat: "json",
	}
}

type recordingTranscriber interface {
	Transcribe(context.Context, string, string, bool) (transcriptionResult, error)
}

// transcriptionEndpoints are tried in order; the first successful response
// wins. Reflection cannot tell whether llm.int is managed OpenAI or a ChatGPT
// subscription, so the response is the only reliable signal.
var transcriptionEndpoints = []string{
	"https://llm.int.exe.xyz/v1/audio/transcriptions",
	"https://openai.int.exe.xyz/v1/audio/transcriptions",
}

type openAIRecordingTranscriber struct {
	client    *http.Client
	endpoints []string
}

type transcriptionAPIResponse struct {
	Text string
	Body []byte
}

func newOpenAIRecordingTranscriber() recordingTranscriber {
	return &openAIRecordingTranscriber{
		client:    http.DefaultClient,
		endpoints: transcriptionEndpoints,
	}
}

func (t *openAIRecordingTranscriber) Transcribe(ctx context.Context, mediaPath, prompt string, timestamps bool) (transcriptionResult, error) {
	transcriptOptions := directTranscriptionOptions(false)
	if !timestamps {
		response, err := t.transcribe(ctx, mediaPath, prompt, transcriptOptions)
		if err != nil {
			return transcriptionResult{}, err
		}
		return transcriptionResult{Text: response.Text, Model: transcriptOptions.Model}, nil
	}

	timestampOptions := directTranscriptionOptions(true)
	var transcriptResponse, timestampResponse transcriptionAPIResponse
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		transcriptResponse, err = t.transcribe(groupContext, mediaPath, prompt, transcriptOptions)
		return err
	})
	group.Go(func() error {
		var err error
		timestampResponse, err = t.transcribe(groupContext, mediaPath, prompt, timestampOptions)
		return err
	})
	if err := group.Wait(); err != nil {
		return transcriptionResult{}, err
	}

	timestampsPath := mediaPath + ".timestamps.json"
	if err := os.WriteFile(timestampsPath, timestampResponse.Body, 0o600); err != nil {
		return transcriptionResult{}, fmt.Errorf("write transcription timestamps: %w", err)
	}
	return transcriptionResult{
		Text:            transcriptResponse.Text,
		Model:           transcriptOptions.Model,
		TimestampsModel: timestampOptions.Model,
		TimestampsPath:  timestampsPath,
	}, nil
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
	for _, endpoint := range t.endpoints {
		response, err = t.request(ctx, endpoint, contentType, body.Bytes())
		if err == nil {
			break
		}
	}
	return response, err
}

func (t *openAIRecordingTranscriber) request(ctx context.Context, endpoint, contentType string, body []byte) (transcriptionAPIResponse, error) {
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
		return transcriptionAPIResponse{}, fmt.Errorf("OpenAI transcription failed (%s): %s", resp.Status, message)
	}

	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return transcriptionAPIResponse{}, fmt.Errorf("decode OpenAI transcription response: %w", err)
	}
	decoded.Text = strings.TrimSpace(decoded.Text)
	if decoded.Text == "" {
		return transcriptionAPIResponse{}, errors.New("OpenAI transcription returned an empty transcript")
	}
	return transcriptionAPIResponse{Text: decoded.Text, Body: responseBody}, nil
}
