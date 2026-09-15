package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIRecordingTranscriber(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "direct.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got := r.FormValue("model"); got != openAITranscriptionModel {
			t.Errorf("model = %q", got)
		}
		if got := r.FormValue("response_format"); got != "json" {
			t.Errorf("response_format = %q", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if header.Filename != "direct.webm" {
			t.Errorf("filename = %q", header.Filename)
		}
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "media" {
			t.Errorf("file = %q", data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":" immediate words "}`)
	}))
	defer api.Close()

	transcriber := &openAIRecordingTranscriber{
		client: api.Client(), endpoint: api.URL, apiKey: func() string { return "test-key" },
	}
	result, err := transcriber.Transcribe(t.Context(), mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "immediate words" || result.Model != openAITranscriptionModel {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAIRecordingTranscriberRequiresAPIKey(t *testing.T) {
	transcriber := &openAIRecordingTranscriber{
		client: http.DefaultClient, endpoint: "unused", apiKey: func() string { return "" },
	}
	_, err := transcriber.Transcribe(t.Context(), "unused")
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIRecordingTranscriberReportsAPIError(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "error.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"unsupported recording"}}`)
	}))
	defer api.Close()

	transcriber := &openAIRecordingTranscriber{
		client: api.Client(), endpoint: api.URL, apiKey: func() string { return "test-key" },
	}
	_, err := transcriber.Transcribe(context.Background(), mediaPath)
	if err == nil || !strings.Contains(err.Error(), "unsupported recording") {
		t.Fatalf("error = %v", err)
	}
}
