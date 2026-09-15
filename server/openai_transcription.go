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
)

const (
	openAITranscriptionEndpoint = "https://openai.int.exe.xyz/v1/audio/transcriptions"
	openAITranscriptionModel    = "gpt-4o-transcribe"
	maxTranscriptionErrorBody   = 64 << 10
	maxTranscriptionResponse    = 16 << 20
)

type transcriptionResult struct {
	Text  string
	Model string
}

type recordingTranscriber interface {
	Transcribe(context.Context, string) (transcriptionResult, error)
}

type openAIRecordingTranscriber struct {
	client   *http.Client
	endpoint string
}

func newOpenAIRecordingTranscriber() recordingTranscriber {
	return &openAIRecordingTranscriber{
		client:   http.DefaultClient,
		endpoint: openAITranscriptionEndpoint,
	}
}

func (t *openAIRecordingTranscriber) Transcribe(ctx context.Context, mediaPath string) (transcriptionResult, error) {
	media, err := os.Open(mediaPath)
	if err != nil {
		return transcriptionResult{}, fmt.Errorf("open recording: %w", err)
	}
	defer media.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", openAITranscriptionModel); err != nil {
		return transcriptionResult{}, err
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return transcriptionResult{}, err
	}
	part, err := writer.CreateFormFile("file", filepath.Base(mediaPath))
	if err != nil {
		return transcriptionResult{}, err
	}
	if _, err := io.Copy(part, media); err != nil {
		return transcriptionResult{}, fmt.Errorf("read recording: %w", err)
	}
	if err := writer.Close(); err != nil {
		return transcriptionResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, &body)
	if err != nil {
		return transcriptionResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := t.client.Do(req)
	if err != nil {
		return transcriptionResult{}, fmt.Errorf("OpenAI transcription request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTranscriptionResponse+1))
	if err != nil {
		return transcriptionResult{}, fmt.Errorf("read OpenAI transcription response: %w", err)
	}
	if len(responseBody) > maxTranscriptionResponse {
		return transcriptionResult{}, errors.New("OpenAI transcription response exceeded 16 MiB")
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
		return transcriptionResult{}, fmt.Errorf("OpenAI transcription failed (%s): %s", resp.Status, message)
	}

	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return transcriptionResult{}, fmt.Errorf("decode OpenAI transcription response: %w", err)
	}
	decoded.Text = strings.TrimSpace(decoded.Text)
	if decoded.Text == "" {
		return transcriptionResult{}, errors.New("OpenAI transcription returned an empty transcript")
	}
	return transcriptionResult{Text: decoded.Text, Model: openAITranscriptionModel}, nil
}
