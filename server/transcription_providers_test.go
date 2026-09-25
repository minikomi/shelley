package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/transcription"
)

type transcriptionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f transcriptionRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func providerRecording(t *testing.T, name string) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte("provider media"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func providerModel(protocol transcription.Protocol, endpoint string) transcription.Model {
	return transcription.Model{
		ID: "configured", Protocol: protocol, Provider: string(protocol), Endpoint: endpoint,
		APIKey: "provider-secret", Model: "selected-model", SupportsPrompted: protocol == transcription.ProtocolOpenAI, SupportsTimecodes: true,
	}
}

func TestRegisterBuiltinTranscriptionProvidersRegistersBothProtocols(t *testing.T) {
	server, _, _ := newTestServer(t)
	if err := server.RegisterBuiltinTranscriptionProviders(); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []transcription.Protocol{transcription.ProtocolOpenAI, transcription.ProtocolDeepgram} {
		if server.transcriptionProviders[protocol] == nil {
			t.Fatalf("provider %q was not registered", protocol)
		}
	}
	if err := server.RegisterBuiltinTranscriptionProviders(); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration error = %v", err)
	}
}

func TestOpenAITranscriptionProviderKeepsPromptAndTimecodesSeparate(t *testing.T) {
	mediaPath := providerRecording(t, "recording.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != -1 {
			t.Errorf("OpenAI multipart content length = %d, want streamed body", r.ContentLength)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		switch r.FormValue("model") {
		case openAITranscriptionModel:
			if got := r.FormValue("prompt"); got != "context for canonical transcript" {
				t.Errorf("prompt = %q", got)
			}
			_, _ = io.WriteString(w, `{"text":"canonical words"}`)
		case openAITimestampedTranscriptionModel:
			if _, exists := r.MultipartForm.Value["prompt"]; exists {
				t.Errorf("Whisper request unexpectedly has prompt: %#v", r.MultipartForm.Value["prompt"])
			}
			if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 2 || got[0] != "word" || got[1] != "segment" {
				t.Errorf("timestamp granularities = %#v", got)
			}
			_, _ = io.WriteString(w, `{"text":"literal words","words":[],"segments":[]}`)
		default:
			t.Errorf("model = %q", r.FormValue("model"))
		}
	}))
	defer api.Close()

	provider := newOpenAITranscriptionProvider(api.Client(), nil)
	promptedModel := providerModel(transcription.ProtocolOpenAI, api.URL)
	promptedModel.Model = openAITranscriptionModel
	prompted, err := provider.Transcribe(t.Context(), transcription.Request{
		Model: promptedModel, Role: transcription.RoleTranscript, RecordingPath: mediaPath, Prompt: "context for canonical transcript",
	})
	if err != nil || prompted.Transcript != "canonical words" {
		t.Fatalf("prompted result=%+v err=%v", prompted, err)
	}
	timecodedModel := promptedModel
	timecodedModel.Model = openAITimestampedTranscriptionModel
	timecoded, err := provider.Transcribe(t.Context(), transcription.Request{
		Model: timecodedModel, Role: transcription.RoleTimecoded, RecordingPath: mediaPath, Prompt: "must not reach Whisper",
	})
	if err != nil || !json.Valid(timecoded.Timecodes) {
		t.Fatalf("timecoded result=%+v err=%v", timecoded, err)
	}
}

func TestOpenAITranscriptionProviderUsesDiarizedJSONForDiarizationModels(t *testing.T) {
	mediaPath := providerRecording(t, "diarized.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got := r.FormValue("model"); got != "gpt-4o-transcribe-diarize" {
			t.Errorf("model = %q", got)
		}
		if got := r.FormValue("response_format"); got != "diarized_json" {
			t.Errorf("response_format = %q", got)
		}
		if got := r.FormValue("chunking_strategy"); got != "auto" {
			t.Errorf("chunking_strategy = %q", got)
		}
		if got := r.MultipartForm.Value["timestamp_granularities[]"]; len(got) != 0 {
			t.Errorf("timestamp granularities = %#v", got)
		}
		if _, exists := r.MultipartForm.Value["prompt"]; exists {
			t.Errorf("diarization request unexpectedly has prompt: %#v", r.MultipartForm.Value["prompt"])
		}
		_, _ = io.WriteString(w, `{"text":"speaker one","segments":[{"speaker":"A","start":0.0,"end":0.8,"text":"speaker one"}]}`)
	}))
	defer api.Close()

	model := providerModel(transcription.ProtocolOpenAI, api.URL)
	model.Model = "gpt-4o-transcribe-diarize"
	result, err := newOpenAITranscriptionProvider(api.Client(), nil).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTimecoded, RecordingPath: mediaPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result.Timecodes) || !strings.Contains(string(result.Timecodes), `"speaker":"A"`) {
		t.Fatalf("timecodes = %s", result.Timecodes)
	}
}

func TestOpenAITranscriptionProviderProfilesRequestShapes(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		profile          transcription.APIProfile
		role             transcription.Role
		responseFormat   string
		chunkingStrategy string
		granularities    []string
		prompt           string
		response         string
	}{
		{
			name: "gpt_4o_mini_auto_json", model: "gpt-4o-mini-transcribe", role: transcription.RoleTranscript,
			responseFormat: "json", response: `{"text":"canonical"}`,
		},
		{
			name: "whisper_auto", model: "whisper-1", role: transcription.RoleTimecoded,
			responseFormat: "verbose_json", granularities: []string{"word", "segment"},
			response: `{"text":"words","words":[],"segments":[]}`,
		},
		{
			name: "whisper_prompted_uses_verbose_json", model: "whisper-1", role: transcription.RoleTranscript,
			responseFormat: "verbose_json", prompt: "context", response: `{"text":"canonical"}`,
		},
		{
			name: "diarized_auto", model: "gpt-4o-transcribe-diarize", role: transcription.RoleTimecoded,
			responseFormat: "diarized_json", chunkingStrategy: "auto",
			response: `{"text":"speaker","segments":[]}`,
		},
		{
			name: "provider_prefix_uses_basename", model: "openai/gpt-4o-mini-transcribe", role: transcription.RoleTranscript,
			responseFormat: "json", response: `{"text":"canonical"}`,
		},
		{
			name: "versioned_gpt_4o_mini_uses_json", model: "gpt-4o-mini-transcribe-2025-12-15", role: transcription.RoleTranscript,
			responseFormat: "json", response: `{"text":"canonical"}`,
		},
		{
			name: "explicit_profile_overrides_model_inference", model: "whisper-1", profile: transcription.APIProfileOpenAIDiarized, role: transcription.RoleTimecoded,
			responseFormat: "diarized_json", chunkingStrategy: "auto",
			response: `{"text":"speaker","segments":[]}`,
		},
		{
			name: "unknown_auto_preserves_prompted_json", model: "custom-transcriber", role: transcription.RoleTranscript,
			responseFormat: "json", response: `{"text":"canonical"}`,
		},
		{
			name: "unknown_auto_preserves_timecoded_whisper", model: "custom-transcriber", role: transcription.RoleTimecoded,
			responseFormat: "verbose_json", granularities: []string{"word", "segment"},
			response: `{"text":"words","words":[],"segments":[]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mediaPath := providerRecording(t, "recording.webm")
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				if got := r.FormValue("model"); got != test.model {
					t.Errorf("model = %q, want %q", got, test.model)
				}
				if got := r.FormValue("response_format"); got != test.responseFormat {
					t.Errorf("response_format = %q, want %q", got, test.responseFormat)
				}
				if got := r.FormValue("chunking_strategy"); got != test.chunkingStrategy {
					t.Errorf("chunking_strategy = %q, want %q", got, test.chunkingStrategy)
				}
				if got := r.MultipartForm.Value["timestamp_granularities[]"]; !equalStrings(got, test.granularities) {
					t.Errorf("timestamp granularities = %#v, want %#v", got, test.granularities)
				}
				if got := r.FormValue("prompt"); got != test.prompt {
					t.Errorf("prompt = %q, want %q", got, test.prompt)
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer api.Close()

			model := providerModel(transcription.ProtocolOpenAI, api.URL)
			model.Model = test.model
			model.APIProfile = test.profile
			result, err := newOpenAITranscriptionProvider(api.Client(), nil).Transcribe(t.Context(), transcription.Request{
				Model: model, Role: test.role, RecordingPath: mediaPath, Prompt: test.prompt,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.role == transcription.RoleTimecoded && !json.Valid(result.Timecodes) {
				t.Fatalf("timecodes = %s", result.Timecodes)
			}
		})
	}
}

func TestOpenAITranscriptionProviderAutoSelectsBase64JSONForFishAudio(t *testing.T) {
	mediaPath := providerRecording(t, "recording.webm")
	converted := false
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ffmpeg" {
			return nil, errors.New("unexpected media command")
		}
		converted = true
		return nil, os.WriteFile(args[len(args)-1], []byte("WAV provider media"), 0o600)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type = %q", got)
		}
		var body struct {
			Model      string  `json:"model"`
			Prompt     *string `json:"prompt"`
			InputAudio struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "fish-audio/transcribe-1" || body.InputAudio.Format != "wav" || body.Prompt != nil {
			t.Fatalf("body = %+v", body)
		}
		audio, err := base64.StdEncoding.DecodeString(body.InputAudio.Data)
		if err != nil || string(audio) != "WAV provider media" {
			t.Fatalf("audio = %q, err = %v", audio, err)
		}
		_, _ = io.WriteString(w, `{"text":"fish transcript"}`)
	}))
	defer api.Close()

	model := providerModel(transcription.ProtocolOpenAI, api.URL)
	model.Model = "fish-audio/transcribe-1"
	model.SupportsPrompted = false
	model.SupportsTimecodes = false
	result, err := newOpenAITranscriptionProvider(api.Client(), runner).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTranscript, RecordingPath: mediaPath, Prompt: "optional context is unavailable",
	})
	if err != nil || result.Transcript != "fish transcript" || !converted {
		t.Fatalf("result = %+v, converted = %v, err = %v", result, converted, err)
	}
}

func TestOpenAITranscriptionProviderRejectsContradictoryBase64PromptCapability(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    string
		encoding transcription.RequestEncoding
	}{
		{name: "auto", model: "fish-audio/transcribe-1", encoding: transcription.RequestEncodingAuto},
		{name: "explicit", model: "custom-transcriber", encoding: transcription.RequestEncodingBase64JSON},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: transcriptionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unsupported prompt reached the provider")
				return nil, errors.New("unexpected transcription request")
			})}
			runner := func(context.Context, string, ...string) ([]byte, error) {
				t.Error("unsupported prompt triggered media processing")
				return nil, errors.New("unexpected media command")
			}
			model := providerModel(transcription.ProtocolOpenAI, "https://transcription.example/v1/audio/transcriptions")
			model.Model = test.model
			model.RequestEncoding = test.encoding
			_, err := newOpenAITranscriptionProvider(client, runner).Transcribe(t.Context(), transcription.Request{
				Model: model, Role: transcription.RoleTranscript, RecordingPath: "unused.webm", Prompt: "Keep names",
			})
			if err == nil || !strings.Contains(err.Error(), "does not support sending prompts") {
				t.Fatalf("prompt error = %v", err)
			}
		})
	}
}

func TestOpenAITranscriptionProviderSendsVerboseJSONForBase64Timecodes(t *testing.T) {
	mediaPath := providerRecording(t, "recording.wav")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model          string `json:"model"`
			ResponseFormat string `json:"response_format"`
			InputAudio     struct {
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "microsoft/mai-transcribe-2" || body.ResponseFormat != "verbose_json" || body.InputAudio.Format != "wav" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = io.WriteString(w, `{"text":"MAI transcript","segments":[{"start":0,"end":1,"text":"MAI transcript"}]}`)
	}))
	defer api.Close()

	model := providerModel(transcription.ProtocolOpenAI, api.URL)
	model.Model = "microsoft/mai-transcribe-2"
	model.APIProfile = transcription.APIProfileOpenAIWhisper
	model.RequestEncoding = transcription.RequestEncodingBase64JSON
	model.SupportsPrompted = false
	result, err := newOpenAITranscriptionProvider(api.Client(), nil).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTimecoded, RecordingPath: mediaPath, Prompt: "not used for timecodes",
	})
	if err != nil || !json.Valid(result.Timecodes) {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestOpenAITranscriptionProviderRejectsProfileRoleMismatches(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile transcription.APIProfile
		role    transcription.Role
		want    string
	}{
		{"json_timecoded", transcription.APIProfileOpenAIJSON, transcription.RoleTimecoded, "transcript-only"},
		{"diarized_unknown", transcription.APIProfileOpenAIDiarized, "invalid", "does not support"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := providerModel(transcription.ProtocolOpenAI, "https://unused.example/v1/audio/transcriptions")
			model.APIProfile = test.profile
			_, err := newOpenAITranscriptionProvider(http.DefaultClient, nil).Transcribe(t.Context(), transcription.Request{
				Model: model, Role: test.role, RecordingPath: providerRecording(t, "recording.wav"),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOpenAITranscriptionProviderValidatesProfileSpecificTimecodes(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    string
		profile  transcription.APIProfile
		response string
		want     string
	}{
		{"whisper_requires_words", "whisper-1", transcription.APIProfileAuto, `{"segments":[]}`, `"words"`},
		{"diarized_requires_segments", "gpt-4o-transcribe-diarize", transcription.APIProfileAuto, `{"words":[]}`, `"segments"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.response)
			}))
			defer api.Close()
			model := providerModel(transcription.ProtocolOpenAI, api.URL)
			model.Model, model.APIProfile = test.model, test.profile
			_, err := newOpenAITranscriptionProvider(api.Client(), nil).Transcribe(t.Context(), transcription.Request{
				Model: model, Role: transcription.RoleTimecoded, RecordingPath: providerRecording(t, "recording.wav"),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOpenAITranscriptionProviderPreparesOversizedRecordingBeforeStreaming(t *testing.T) {
	mediaPath := t.TempDir() + "/oversized.webm"
	file, err := os.Create(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxTranscriptionUpload + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	prepared := false
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ffmpeg" {
			return nil, errors.New("unexpected media command")
		}
		prepared = true
		return nil, os.WriteFile(args[len(args)-1], []byte("prepared audio"), 0o600)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != -1 {
			t.Errorf("content length = %d, want streamed multipart body", r.ContentLength)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		upload, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer upload.Close()
		contents, err := io.ReadAll(upload)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != "prepared audio" {
			t.Errorf("uploaded contents = %q", contents)
		}
		_, _ = io.WriteString(w, `{"text":"prepared transcript"}`)
	}))
	defer api.Close()
	model := providerModel(transcription.ProtocolOpenAI, api.URL)
	model.Model = openAITranscriptionModel
	result, err := newOpenAITranscriptionProvider(api.Client(), runner).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTranscript, RecordingPath: mediaPath,
	})
	if err != nil || result.Transcript != "prepared transcript" || !prepared {
		t.Fatalf("result=%+v prepared=%v err=%v", result, prepared, err)
	}
}

func TestDeepgramManagedImplicitCredentialStreamsWithoutAuthorization(t *testing.T) {
	mediaPath := providerRecording(t, "managed.wav")
	client := &http.Client{Transport: transcriptionRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" {
			t.Errorf("authorization = %q, want edge-managed request", request.Header.Get("Authorization"))
		}
		if _, ok := request.Body.(*os.File); !ok {
			t.Errorf("request body type = %T, want streaming file", request.Body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"results":{"channels":[{"alternatives":[{"transcript":"managed","words":[]}]}],"utterances":[]}
			}`)),
			Request: request,
		}, nil
	})}
	model := providerModel(transcription.ProtocolDeepgram, "https://deepgram.int.example/v1/listen")
	model.Managed = true
	model.APIKey = "implicit"
	result, err := newDeepgramTranscriptionProvider(client).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTimecoded, RecordingPath: mediaPath,
	})
	if err != nil || !json.Valid(result.Timecodes) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDeepgramTranscriptionProviderRequestAndNeutralTimecodes(t *testing.T) {
	mediaPath := providerRecording(t, "recording.webm")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Token provider-secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "video/webm" {
			t.Errorf("content type = %q", got)
		}
		if got := r.ContentLength; got != int64(len("provider media")) {
			t.Errorf("content length = %d", got)
		}
		query := r.URL.Query()
		for key, want := range map[string]string{"model": "selected-model", "mip_opt_out": "true", "punctuate": "true", "utterances": "true"} {
			if got := query.Get(key); got != want {
				t.Errorf("query %s = %q, want %q", key, got, want)
			}
		}
		if query.Has("prompt") || query.Has("keyterm") || query.Has("keywords") {
			t.Errorf("unsupported prompt/keyterm query was sent: %s", r.URL.RawQuery)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "provider media" {
			t.Errorf("raw body = %q", body)
		}
		_, _ = io.WriteString(w, `{
			"results": {
				"channels": [{"alternatives": [{"transcript":"hello Shelley", "words":[
					{"word":"hello","start":0.0,"end":0.3}, {"word":"Shelley","start":0.3,"end":0.8}
				]}]}],
				"utterances": [{"transcript":"Hello Shelley.","start":0.0,"end":0.8}]
			}
		}`)
	}))
	defer api.Close()

	provider := newDeepgramTranscriptionProvider(api.Client())
	result, err := provider.Transcribe(t.Context(), transcription.Request{
		Model: providerModel(transcription.ProtocolDeepgram, api.URL+"?mip_opt_out=false"), Role: transcription.RoleTimecoded,
		RecordingPath: mediaPath, Prompt: "arbitrary Shelley-specific context is not a keyterm list",
	})
	if err != nil {
		t.Fatal(err)
	}
	var timestamps neutralTranscriptionTimecodes
	if err := json.Unmarshal(result.Timecodes, &timestamps); err != nil {
		t.Fatal(err)
	}
	if result.Transcript != "hello Shelley" || timestamps.Text != "hello Shelley" ||
		len(timestamps.Words) != 2 || timestamps.Words[1].Word != "Shelley" ||
		len(timestamps.Segments) != 1 || timestamps.Segments[0].Text != "Hello Shelley." {
		t.Fatalf("result=%+v timestamps=%+v", result, timestamps)
	}
}

func TestDeepgramTranscriptionProviderRejectsMismatchedPromptCapability(t *testing.T) {
	mediaPath := providerRecording(t, "recording.wav")
	model := providerModel(transcription.ProtocolDeepgram, "https://unused.example/v1/listen")
	model.SupportsPrompted = true
	_, err := newDeepgramTranscriptionProvider(http.DefaultClient).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTimecoded, RecordingPath: mediaPath,
	})
	if err == nil || !strings.Contains(err.Error(), "must not advertise") {
		t.Fatalf("mismatched capability error = %v", err)
	}
}

func TestOpenAITranscriptionProviderTranscriptWithoutContextSupport(t *testing.T) {
	for _, test := range []struct {
		model, responseFormat string
	}{
		{model: "gpt-transcribe", responseFormat: "json"},
		{model: "whisper-1", responseFormat: "verbose_json"},
		{model: "gpt-4o-transcribe-diarize", responseFormat: "diarized_json"},
	} {
		t.Run(test.model, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				if _, ok := r.MultipartForm.Value["prompt"]; ok {
					t.Error("sent context to a model without prompt support")
				}
				if got := r.FormValue("response_format"); got != test.responseFormat {
					t.Errorf("response format = %q", got)
				}
				_, _ = io.WriteString(w, `{"text":"plain transcript"}`)
			}))
			defer api.Close()
			model := providerModel(transcription.ProtocolOpenAI, api.URL)
			model.Model = test.model
			model.SupportsPrompted = false
			model.SupportsTimecodes = false
			result, err := newOpenAITranscriptionProvider(api.Client(), nil).Transcribe(t.Context(), transcription.Request{
				Model: model, Role: transcription.RoleTranscript,
				RecordingPath: providerRecording(t, "recording.wav"), Prompt: "optional context",
			})
			if err != nil || result.Transcript != "plain transcript" || len(result.Timecodes) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDeepgramTranscriptionProviderTranscriptWithoutContextSupport(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		for _, key := range []string{"prompt", "keyterm", "keywords", "utterances"} {
			if query.Has(key) {
				t.Errorf("unexpected query field %q", key)
			}
		}
		_, _ = io.WriteString(w, `{"results":{"channels":[{"alternatives":[{"transcript":"plain transcript"}]}]}}`)
	}))
	defer api.Close()
	model := providerModel(transcription.ProtocolDeepgram, api.URL)
	model.SupportsTimecodes = false
	result, err := newDeepgramTranscriptionProvider(api.Client()).Transcribe(t.Context(), transcription.Request{
		Model: model, Role: transcription.RoleTranscript,
		RecordingPath: providerRecording(t, "recording.wav"), Prompt: "optional context",
	})
	if err != nil || result.Transcript != "plain transcript" || len(result.Timecodes) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDeepgramTranscriptionProviderCredentialsAndUnsupportedCapabilities(t *testing.T) {
	provider := newDeepgramTranscriptionProvider(http.DefaultClient)
	model := providerModel(transcription.ProtocolDeepgram, "https://unused.example/v1/listen")
	model.APIKey = ""
	_, err := provider.Transcribe(t.Context(), transcription.Request{Model: model, Role: transcription.RoleTimecoded, RecordingPath: providerRecording(t, "recording.wav")})
	if err == nil || !strings.Contains(err.Error(), "custom Deepgram") {
		t.Fatalf("missing credential error = %v", err)
	}
	model.APIKey = "provider-secret"
	model.SupportsTimecodes = false
	_, err = provider.Transcribe(t.Context(), transcription.Request{Model: model, Role: transcription.RoleTimecoded, RecordingPath: providerRecording(t, "recording.wav")})
	if err == nil || !strings.Contains(err.Error(), "does not support timecoded") {
		t.Fatalf("capability error = %v", err)
	}
}

func TestDeepgramTranscriptionProviderRejectsMalformedTimestampResponses(t *testing.T) {
	for name, response := range map[string]string{
		"missing_alternative": `{"results":{"channels":[]}}`,
		"missing_words":       `{"results":{"channels":[{"alternatives":[{"transcript":"words"}]}],"utterances":[]}}`,
		"missing_utterances":  `{"results":{"channels":[{"alternatives":[{"transcript":"words","words":[]}]}]}}`,
		"invalid_range":       `{"results":{"channels":[{"alternatives":[{"transcript":"words","words":[{"word":"words","start":1,"end":0}]}]}],"utterances":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, response)
			}))
			defer api.Close()
			_, err := newDeepgramTranscriptionProvider(api.Client()).Transcribe(t.Context(), transcription.Request{
				Model: providerModel(transcription.ProtocolDeepgram, api.URL), Role: transcription.RoleTimecoded,
				RecordingPath: providerRecording(t, "recording.wav"),
			})
			if err == nil || !strings.Contains(err.Error(), "Deepgram transcription response") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDeepgramTranscriptionProviderCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := newDeepgramTranscriptionProvider(api.Client()).Transcribe(ctx, transcription.Request{
			Model: providerModel(transcription.ProtocolDeepgram, api.URL), Role: transcription.RoleTimecoded,
			RecordingPath: providerRecording(t, "recording.wav"),
		})
		errCh <- err
	}()
	<-started
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("error = %v", err)
	}
	close(release)
}

func TestDeepgramTranscriptionProviderBoundsResponse(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxTranscriptionResponse+1))
	}))
	defer api.Close()
	_, err := newDeepgramTranscriptionProvider(api.Client()).Transcribe(t.Context(), transcription.Request{
		Model: providerModel(transcription.ProtocolDeepgram, api.URL), Role: transcription.RoleTimecoded,
		RecordingPath: providerRecording(t, "recording.wav"),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded 16 MiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepgramTranscriptionProviderUsesAbsoluteURL(t *testing.T) {
	_, err := newDeepgramTranscriptionProvider(http.DefaultClient).Transcribe(t.Context(), transcription.Request{
		Model: providerModel(transcription.ProtocolDeepgram, "://bad"), Role: transcription.RoleTimecoded,
		RecordingPath: providerRecording(t, "recording.wav"),
	})
	if err == nil || !strings.Contains(err.Error(), "parse Deepgram endpoint") {
		t.Fatalf("error = %v", err)
	}
}
