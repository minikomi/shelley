package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOpenAIRecordingTranscriber(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "direct.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization should be injected by the proxy, got %q", got)
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
		if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 0 {
			t.Errorf("timestamp granularities = %#v", got)
		}
		if got := r.FormValue("prompt"); got != "Shelley on example-vm" {
			t.Errorf("prompt = %q", got)
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

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoint: api.URL}
	result, err := transcriber.Transcribe(t.Context(), mediaPath, "Shelley on example-vm", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "immediate words" || result.Model != openAITranscriptionModel {
		t.Fatalf("result = %#v", result)
	}
	if result.TimestampsPath != "" {
		t.Fatalf("unexpected timestamps path = %q", result.TimestampsPath)
	}
}

func TestOpenAIRecordingTranscriberWithTimestamps(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "screen.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got := r.FormValue("model"); got != openAITimestampedTranscriptionModel {
			t.Errorf("model = %q", got)
		}
		if got := r.FormValue("response_format"); got != "verbose_json" {
			t.Errorf("response_format = %q", got)
		}
		if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 2 || got[0] != "word" || got[1] != "segment" {
			t.Errorf("timestamp granularities = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":" screen words ","words":[{"word":"screen","start":0.0,"end":0.4}],"segments":[{"text":"screen words","start":0.0,"end":0.8}]}`)
	}))
	defer api.Close()

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoint: api.URL}
	result, err := transcriber.Transcribe(t.Context(), mediaPath, "Shelley on example-vm", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "screen words" || result.Model != openAITimestampedTranscriptionModel {
		t.Fatalf("result = %#v", result)
	}
	wantTimestampsPath := mediaPath + ".timestamps.json"
	if result.TimestampsPath != wantTimestampsPath {
		t.Fatalf("timestamps path = %q, want %q", result.TimestampsPath, wantTimestampsPath)
	}
	timestamps, err := os.ReadFile(wantTimestampsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timestamps), `"words"`) || !strings.Contains(string(timestamps), `"segments"`) {
		t.Fatalf("timestamps = %s", timestamps)
	}
}

func TestOpenAIRecordingTranscriberReportsAPIError(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "error.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"unsupported recording"}}`)
	}))
	defer api.Close()

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoint: api.URL}
	_, err := transcriber.Transcribe(context.Background(), mediaPath, "context", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported recording") {
		t.Fatalf("error = %v", err)
	}
}
