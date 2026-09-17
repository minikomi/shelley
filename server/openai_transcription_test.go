package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoints: []string{api.URL}}
	result, err := transcriber.Transcribe(t.Context(), mediaPath, "Shelley on example-vm", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "immediate words" || result.Model != openAITranscriptionModel {
		t.Fatalf("result = %#v", result)
	}
	if result.TimestampsModel != "" || result.TimestampsPath != "" {
		t.Fatalf("unexpected timestamps result = %#v", result)
	}
}

func TestOpenAIRecordingTranscriberWithTimestamps(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "screen.webm")
	var gptRequests, whisperRequests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		switch model := r.FormValue("model"); model {
		case openAITranscriptionModel:
			gptRequests.Add(1)
			if got := r.FormValue("response_format"); got != "json" {
				t.Errorf("GPT response_format = %q", got)
			}
			if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 0 {
				t.Errorf("GPT timestamp granularities = %#v", got)
			}
			_, _ = io.WriteString(w, `{"text":" adjusted GPT words "}`)
		case openAITimestampedTranscriptionModel:
			whisperRequests.Add(1)
			if got := r.FormValue("response_format"); got != "verbose_json" {
				t.Errorf("Whisper response_format = %q", got)
			}
			if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 2 || got[0] != "word" || got[1] != "segment" {
				t.Errorf("Whisper timestamp granularities = %#v", got)
			}
			_, _ = io.WriteString(w, `{"text":" literal whisper words ","words":[{"word":"literal","start":0.0,"end":0.4}],"segments":[{"text":"literal whisper words","start":0.0,"end":0.8}]}`)
		default:
			t.Errorf("unexpected model = %q", model)
		}
		w.Header().Set("Content-Type", "application/json")
	}))
	defer api.Close()

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoints: []string{api.URL}}
	result, err := transcriber.Transcribe(t.Context(), mediaPath, "Shelley on example-vm", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "adjusted GPT words" ||
		result.Model != openAITranscriptionModel ||
		result.TimestampsModel != openAITimestampedTranscriptionModel {
		t.Fatalf("result = %#v", result)
	}
	if gptRequests.Load() != 1 || whisperRequests.Load() != 1 {
		t.Fatalf("requests: GPT=%d Whisper=%d", gptRequests.Load(), whisperRequests.Load())
	}
	wantTimestampsPath := mediaPath + ".timestamps.json"
	if result.TimestampsPath != wantTimestampsPath {
		t.Fatalf("timestamps path = %q, want %q", result.TimestampsPath, wantTimestampsPath)
	}
	timestamps, err := os.ReadFile(wantTimestampsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timestamps), `"literal whisper words"`) ||
		!strings.Contains(string(timestamps), `"words"`) ||
		!strings.Contains(string(timestamps), `"segments"`) {
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

	transcriber := &openAIRecordingTranscriber{client: api.Client(), endpoints: []string{api.URL}}
	_, err := transcriber.Transcribe(context.Background(), mediaPath, "context", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported recording") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIRecordingTranscriberTriesEndpointsInOrder(t *testing.T) {
	mediaPath := transcriptionTestFile(t, "order.webm")
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `ChatGPT subscriptions do not support transcription`)
	}))
	defer rejecting.Close()
	accepting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"second gateway"}`)
	}))
	defer accepting.Close()

	transcriber := &openAIRecordingTranscriber{client: rejecting.Client(), endpoints: []string{rejecting.URL, accepting.URL}}
	result, err := transcriber.Transcribe(context.Background(), mediaPath, "context", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "second gateway" {
		t.Fatalf("text = %q", result.Text)
	}
}
