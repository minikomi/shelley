package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
	"shelley.exe.dev/transcription"
)

type transcriptionCatalogTestManager struct {
	*testLLMManager
	managed []models.TranscriptionModel
}

func (m *transcriptionCatalogTestManager) GetManagedTranscriptionModels() []models.TranscriptionModel {
	return append([]models.TranscriptionModel(nil), m.managed...)
}

type transcriptionProviderFunc struct {
	protocol transcription.Protocol
	calls    []transcription.Request
	fn       func(transcription.Request) (transcription.Result, error)
}

func (p *transcriptionProviderFunc) Protocol() transcription.Protocol { return p.protocol }
func (p *transcriptionProviderFunc) Transcribe(_ context.Context, request transcription.Request) (transcription.Result, error) {
	p.calls = append(p.calls, request)
	return p.fn(request)
}

func transcriptionAPIServer(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()
	server, _, _ := newTestServer(t)
	server.llmManager = &transcriptionCatalogTestManager{
		testLLMManager: server.llmManager.(*testLLMManager),
		managed: []models.TranscriptionModel{{
			ID: "managed-openai", DisplayName: "Managed OpenAI", Protocol: transcription.ProtocolOpenAI,
			Provider: "openai", Endpoint: "https://managed.example/v1/audio/transcriptions", APIKey: "managed-secret",
			Model: "gpt-transcribe", SupportsPrompted: true, Managed: true, Source: "llm.int.example",
		}},
	}
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	return server, mux
}

func doTranscriptionAPIRequest(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func TestTranscriptionModelAPILifecycleAndSecretSanitization(t *testing.T) {
	server, mux := transcriptionAPIServer(t)
	createdResponse := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", `{
		"display_name":"Custom OpenAI",
		"protocol":"openai",
		"provider":"openai",
		"endpoint":"https://api.openai.com/v1/audio/transcriptions",
		"api_key":"custom-secret",
		"model_name":"gpt-transcribe",
		"api_profile":"openai-whisper",
		"supports_prompted":true,
		"supports_timecodes":true
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	if strings.Contains(createdResponse.Body.String(), "custom-secret") || strings.Contains(createdResponse.Body.String(), `"api_key":`) {
		t.Fatalf("create exposed secret: %s", createdResponse.Body.String())
	}
	var created TranscriptionModelAPI
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ModelID == "" || created.APIProfile != transcription.APIProfileOpenAIWhisper || !created.HasAPIKey || created.Managed {
		t.Fatalf("created = %+v", created)
	}

	get := doTranscriptionAPIRequest(t, mux, http.MethodGet, "/api/transcription-models/"+created.ModelID, "")
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "custom-secret") {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	updated := doTranscriptionAPIRequest(t, mux, http.MethodPut, "/api/transcription-models/"+created.ModelID, `{
		"display_name":"Updated OpenAI",
		"protocol":"openai",
		"provider":"openai",
		"endpoint":"https://api.openai.com/v1/audio/transcriptions",
		"model_name":"gpt-transcribe"
	}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	stored, err := server.db.GetTranscriptionModel(t.Context(), created.ModelID)
	if err != nil || stored.ApiKey != "custom-secret" || stored.ApiProfile != string(transcription.APIProfileOpenAIWhisper) {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	redirect := doTranscriptionAPIRequest(t, mux, http.MethodPut, "/api/transcription-models/"+created.ModelID, `{
		"display_name":"Redirected OpenAI",
		"protocol":"openai",
		"provider":"openai",
		"endpoint":"https://attacker.example/collect",
		"model_name":"gpt-transcribe"
	}`)
	if redirect.Code != http.StatusBadRequest || !strings.Contains(redirect.Body.String(), "api_key is required") {
		t.Fatalf("redirect status=%d body=%s", redirect.Code, redirect.Body.String())
	}
	stored, err = server.db.GetTranscriptionModel(t.Context(), created.ModelID)
	if err != nil || stored.Endpoint != "https://api.openai.com/v1/audio/transcriptions" || stored.ApiKey != "custom-secret" {
		t.Fatalf("redirect mutated stored model=%+v err=%v", stored, err)
	}

	duplicate := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models/"+created.ModelID+"/duplicate", `{"display_name":"Duplicate"}`)
	if duplicate.Code != http.StatusCreated || strings.Contains(duplicate.Body.String(), "custom-secret") {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var copied TranscriptionModelAPI
	if err := json.Unmarshal(duplicate.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	if copied.APIProfile != transcription.APIProfileOpenAIWhisper {
		t.Fatalf("copied = %+v", copied)
	}
	copiedStored, err := server.db.GetTranscriptionModel(t.Context(), copied.ModelID)
	if err != nil || copiedStored.ApiKey != "custom-secret" || copiedStored.ApiProfile != string(transcription.APIProfileOpenAIWhisper) {
		t.Fatalf("copied = %+v err=%v", copiedStored, err)
	}

	setPrompted := doTranscriptionAPIRequest(t, mux, http.MethodPut, "/api/transcription-model-defaults/transcript", `{"model_id":"`+created.ModelID+`"}`)
	setTimecoded := doTranscriptionAPIRequest(t, mux, http.MethodPut, "/api/transcription-model-defaults/timecoded", `{"model_id":"`+copied.ModelID+`"}`)
	if setPrompted.Code != http.StatusOK || setTimecoded.Code != http.StatusOK {
		t.Fatalf("defaults prompted=%d %s timecoded=%d %s", setPrompted.Code, setPrompted.Body.String(), setTimecoded.Code, setTimecoded.Body.String())
	}

	list := doTranscriptionAPIRequest(t, mux, http.MethodGet, "/api/transcription-models", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "secret") || strings.Contains(list.Body.String(), `"api_key":`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var catalog TranscriptionCatalogAPI
	if err := json.Unmarshal(list.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 3 || !catalog.Defaults[transcription.RoleTranscript].Available || !catalog.Defaults[transcription.RoleTimecoded].Available {
		t.Fatalf("catalog = %+v", catalog)
	}

	managedDelete := doTranscriptionAPIRequest(t, mux, http.MethodDelete, "/api/transcription-models/managed-openai", "")
	if managedDelete.Code != http.StatusConflict {
		t.Fatalf("managed delete status=%d body=%s", managedDelete.Code, managedDelete.Body.String())
	}
	deleted := doTranscriptionAPIRequest(t, mux, http.MethodDelete, "/api/transcription-models/"+created.ModelID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	list = doTranscriptionAPIRequest(t, mux, http.MethodGet, "/api/transcription-models", "")
	if err := json.Unmarshal(list.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Defaults[transcription.RoleTranscript].Available {
		t.Fatalf("deleted selected default silently remained available: %+v", catalog.Defaults)
	}
}

func TestTranscriptionModelDuplicateValidatesCustomConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		apiKey string
		status int
	}{
		{name: "no key", status: http.StatusBadRequest},
		{name: "implicit key", apiKey: "implicit", status: http.StatusBadRequest},
		{name: "explicit key", apiKey: "managed-secret", status: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, mux := transcriptionAPIServer(t)
			manager := server.llmManager.(*transcriptionCatalogTestManager)
			manager.managed = append(manager.managed, models.TranscriptionModel{
				ID: "managed-deepgram", DisplayName: "Managed Deepgram", Protocol: transcription.ProtocolDeepgram,
				Provider: "deepgram", Endpoint: "https://managed.example/v1/listen", APIKey: test.apiKey,
				Model: "nova-3", SupportsTimecodes: true, Managed: true, Source: "llm.int.example",
			})
			response := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models/managed-deepgram/duplicate", `{}`)
			if response.Code != test.status {
				t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
			}
			rows, err := server.db.ListTranscriptionModels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if test.status == http.StatusBadRequest {
				if !strings.Contains(response.Body.String(), "require an API key") || len(rows) != 0 {
					t.Fatalf("invalid duplicate persisted: body=%s rows=%+v", response.Body.String(), rows)
				}
			} else {
				var copied TranscriptionModelAPI
				if err := json.Unmarshal(response.Body.Bytes(), &copied); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0].ModelID != copied.ModelID || rows[0].Managed ||
					rows[0].Source != models.SourceCustomLabel || rows[0].ApiKey != test.apiKey ||
					copied.DisplayName != "Managed Deepgram (copy)" || copied.Managed || !copied.HasAPIKey {
					t.Fatalf("copied=%+v rows=%+v", copied, rows)
				}
				if strings.Contains(response.Body.String(), test.apiKey) {
					t.Fatalf("duplicate exposed secret: %s", response.Body.String())
				}
				deleted := doTranscriptionAPIRequest(t, mux, http.MethodDelete, "/api/transcription-models/"+copied.ModelID, "")
				if deleted.Code != http.StatusNoContent {
					t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
				}
			}
			list := doTranscriptionAPIRequest(t, mux, http.MethodGet, "/api/transcription-models", "")
			if list.Code != http.StatusOK {
				t.Fatalf("duplicate broke catalog: status=%d body=%s", list.Code, list.Body.String())
			}
			var catalog TranscriptionCatalogAPI
			if err := json.Unmarshal(list.Body.Bytes(), &catalog); err != nil {
				t.Fatal(err)
			}
			if len(catalog.Models) != 2 {
				t.Fatalf("catalog = %+v", catalog)
			}
		})
	}
}

func TestCustomDeepgramModelValidationAndEndpointNormalization(t *testing.T) {
	_, mux := transcriptionAPIServer(t)
	prompted := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", `{
		"display_name":"Deepgram Prompted",
		"protocol":"deepgram",
		"provider":"deepgram",
		"endpoint":"https://api.deepgram.com",
		"api_key":"custom-secret",
		"model_name":"nova-3",
		"supports_prompted":true,
		"supports_timecodes":true
	}`)
	if prompted.Code != http.StatusBadRequest || !strings.Contains(prompted.Body.String(), "context prompts") {
		t.Fatalf("prompted status=%d body=%s", prompted.Code, prompted.Body.String())
	}
	missingKey := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", `{
		"display_name":"Deepgram No Key",
		"protocol":"deepgram",
		"provider":"deepgram",
		"endpoint":"https://api.deepgram.com",
		"model_name":"nova-3",
		"supports_timecodes":true
	}`)
	if missingKey.Code != http.StatusBadRequest || !strings.Contains(missingKey.Body.String(), "require an API key") {
		t.Fatalf("missing key status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	created := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", `{
		"display_name":"Deepgram Timecodes",
		"protocol":"deepgram",
		"provider":"deepgram",
		"endpoint":"https://api.deepgram.com",
		"api_key":"custom-secret",
		"model_name":"nova-3",
		"api_profile":"openai-diarized",
		"supports_timecodes":true
	}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var model TranscriptionModelAPI
	if err := json.Unmarshal(created.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Endpoint != "https://api.deepgram.com/v1/listen" || model.APIProfile != transcription.APIProfileAuto || model.SupportsPrompted || !model.SupportsTimecodes {
		t.Fatalf("created model = %+v", model)
	}
}

func TestTranscriptionExplicitAPIProfileCapabilitiesAreValidated(t *testing.T) {
	_, mux := transcriptionAPIServer(t)
	for _, test := range []struct {
		name, profile       string
		prompted, timecoded bool
	}{
		{name: "json timecoded", profile: "openai-json", prompted: true, timecoded: true},
		{name: "diarized prompted", profile: "openai-diarized", prompted: true, timecoded: true},
		{name: "auto standard timecoded", profile: "auto", prompted: true, timecoded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			modelName := "model"
			if test.name == "auto standard timecoded" {
				modelName = "gpt-4o-mini-transcribe"
			}
			response := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", fmt.Sprintf(`{
				"display_name":"%s",
				"protocol":"openai",
				"provider":"openai",
				"endpoint":"https://api.openai.com/v1/audio/transcriptions",
				"api_key":"custom-secret",
				"model_name":"%s",
				"api_profile":"%s",
				"supports_prompted":%t,
				"supports_timecodes":%t
			}`, test.name, modelName, test.profile, test.prompted, test.timecoded))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestTranscriptionPlainModelsCanBeSelectedForTranscript(t *testing.T) {
	for _, test := range []struct {
		name, model, encoding string
		protocol              transcription.Protocol
	}{
		{name: "multipart", model: "gpt-transcribe", protocol: transcription.ProtocolOpenAI},
		{name: "base64 auto", model: "fish-audio/transcribe-1", protocol: transcription.ProtocolOpenAI},
		{name: "base64 explicit", model: "custom-model", encoding: "base64-json", protocol: transcription.ProtocolOpenAI},
		{name: "diarized", model: "gpt-4o-transcribe-diarize", protocol: transcription.ProtocolOpenAI},
		{name: "deepgram", model: "nova-3", protocol: transcription.ProtocolDeepgram},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, mux := transcriptionAPIServer(t)
			body, err := json.Marshal(CreateTranscriptionModelRequest{
				DisplayName: test.name, Protocol: test.protocol, Endpoint: "https://transcription.example/v1/listen",
				APIKey: "custom-secret", ModelName: test.model, RequestEncoding: transcription.RequestEncoding(test.encoding),
			})
			if err != nil {
				t.Fatal(err)
			}
			response := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", string(body))
			if response.Code != http.StatusCreated {
				t.Fatalf("plain model status=%d body=%s", response.Code, response.Body.String())
			}
			var created TranscriptionModelAPI
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.SupportsPrompted || created.SupportsTimecodes {
				t.Fatalf("plain model capabilities = %+v", created)
			}
			selected := doTranscriptionAPIRequest(t, mux, http.MethodPut, "/api/transcription-model-defaults/transcript",
				fmt.Sprintf(`{"model_id":%q}`, created.ModelID))
			if selected.Code != http.StatusOK {
				t.Fatalf("select status=%d body=%s", selected.Code, selected.Body.String())
			}
			provider := &transcriptionProviderFunc{
				protocol: test.protocol,
				fn: func(request transcription.Request) (transcription.Result, error) {
					if request.Role != transcription.RoleTranscript || request.Prompt != "" {
						t.Fatalf("plain transcript request = %+v", request)
					}
					return transcription.Result{Transcript: "plain transcript"}, nil
				},
			}
			if err := server.RegisterTranscriptionProvider(provider); err != nil {
				t.Fatal(err)
			}
			refs, err := server.selectQueuedTranscriptionModels(t.Context(), false)
			if err != nil {
				t.Fatal(err)
			}
			result, err := server.transcribeSelected(t.Context(), "unused.wav", "optional context", false, refs)
			if err != nil || result.Text != "plain transcript" || len(provider.calls) != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, len(provider.calls))
			}
		})
	}
}

func TestTranscriptionBase64ModelCannotAdvertiseContextPrompts(t *testing.T) {
	for _, test := range []struct{ model, encoding string }{
		{model: "fish-audio/transcribe-1"},
		{model: "custom-model", encoding: "base64-json"},
	} {
		t.Run(test.model, func(t *testing.T) {
			server, mux := transcriptionAPIServer(t)
			body, err := json.Marshal(CreateTranscriptionModelRequest{
				DisplayName: "Invalid prompt capability", Protocol: transcription.ProtocolOpenAI,
				Endpoint: "https://transcription.example/v1/audio/transcriptions", ModelName: test.model,
				RequestEncoding: transcription.RequestEncoding(test.encoding), SupportsPrompted: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := doTranscriptionAPIRequest(t, mux, http.MethodPost, "/api/transcription-models", string(body))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "context prompts") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			rows, err := server.db.ListTranscriptionModels(t.Context())
			if err != nil || len(rows) != 0 {
				t.Fatalf("invalid model persisted: rows=%+v err=%v", rows, err)
			}
		})
	}
}

func TestTranscriptionDraftCannotSendStoredKeyToOverriddenEndpoint(t *testing.T) {
	server, _, _ := newTestServer(t)
	created, err := server.db.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "stored", DisplayName: "Stored", Protocol: "openai", Provider: "openai",
		Endpoint: "https://trusted.example/v1/audio/transcriptions", ApiKey: "stored-secret", ModelName: "gpt-transcribe",
		ApiProfile: string(transcription.APIProfileOpenAIWhisper), SupportsPrompted: true, SupportsTimecodes: true, Source: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.resolveDraftTranscriptionModel(t.Context(), DraftTranscriptionModel{
		ModelID: created.ModelID, Endpoint: "https://attacker.example/collect",
	})
	if err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("override error = %v", err)
	}
	resolved, err := server.resolveDraftTranscriptionModel(t.Context(), DraftTranscriptionModel{
		ModelID: created.ModelID, Endpoint: "https://attacker.example/collect", APIKey: "explicit-draft-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != "explicit-draft-key" || resolved.APIProfile != transcription.APIProfileOpenAIWhisper {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestTranscriptionProviderErrorsAreActionableRedactedAndLogged(t *testing.T) {
	server, _, _ := newTestServer(t)
	var logs bytes.Buffer
	server.logger = slog.New(slog.NewTextHandler(&logs, nil))
	provider := &transcriptionProviderFunc{
		protocol: transcription.ProtocolDeepgram,
		fn: func(transcription.Request) (transcription.Result, error) {
			return transcription.Result{}, errors.New("authentication failed for secret-key and secret%2Dkey")
		},
	}
	if err := server.RegisterTranscriptionProvider(provider); err != nil {
		t.Fatal(err)
	}
	_, err := server.runTranscriptionProvider(t.Context(), transcription.Request{
		Model: transcription.Model{
			ID: "draft", Protocol: transcription.ProtocolDeepgram, Provider: "deepgram", APIKey: "secret-key",
			SupportsTimecodes: true,
		},
		Role: transcription.RoleTimecoded,
	})
	if err == nil || strings.Contains(err.Error(), "secret-key") || !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("safe provider error = %v", err)
	}
	if got := logs.String(); strings.Contains(got, "secret-key") || !strings.Contains(got, "authentication failed") || !strings.Contains(got, "model_id=draft") {
		t.Fatalf("provider log = %q", got)
	}
}

func TestTranscriptionModelTestDraftUsesUploadWithoutPersistence(t *testing.T) {
	server, mux := transcriptionAPIServer(t)
	provider := &transcriptionProviderFunc{
		protocol: transcription.ProtocolOpenAI,
		fn: func(request transcription.Request) (transcription.Result, error) {
			contents, err := os.ReadFile(request.RecordingPath)
			if err != nil {
				return transcription.Result{}, err
			}
			if string(contents) != "short recording" || request.Model.APIKey != "draft-secret" {
				t.Fatalf("request = %+v contents=%q", request, contents)
			}
			if request.Role == transcription.RoleTranscript {
				if request.Prompt != "Keep names" {
					t.Fatalf("transcript context = %q", request.Prompt)
				}
				return transcription.Result{Transcript: "test transcript"}, nil
			}
			if request.Prompt != "" {
				t.Fatalf("timecodes received context: %q", request.Prompt)
			}
			return transcription.Result{
				Transcript: "timecoded transcript",
				Timecodes:  json.RawMessage(`{"words":[],"segments":[]}`),
			}, nil
		},
	}
	if err := server.RegisterTranscriptionProvider(provider); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	draft, _ := json.Marshal(DraftTranscriptionModel{
		DisplayName: "Unsaved", Protocol: transcription.ProtocolOpenAI, Provider: "openai",
		Endpoint: "https://api.openai.com/v1/audio/transcriptions", APIKey: "draft-secret", ModelName: "draft-model",
		SupportsPrompted: boolPtr(true), SupportsTimecodes: boolPtr(true),
	})
	if err := writer.WriteField("model", string(draft)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "Keep names"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "recording.webm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "short recording"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/transcription-models/test", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "draft-secret") || strings.Contains(response.Body.String(), `"api_key":`) {
		t.Fatalf("test response exposed secret: %s", response.Body.String())
	}
	var result TranscriptionModelTestAPI
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 2 || !result.Results[transcription.RoleTranscript].Success || !result.Results[transcription.RoleTimecoded].Success {
		t.Fatalf("calls=%+v result=%+v", provider.calls, result)
	}
	timecoded := result.Results[transcription.RoleTimecoded]
	if timecoded.Transcript != "timecoded transcript" || !json.Valid(timecoded.Timecodes) {
		t.Fatalf("timecoded result=%+v", timecoded)
	}
	rows, err := server.db.ListTranscriptionModels(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("test persisted rows=%+v err=%v", rows, err)
	}
}

func boolPtr(value bool) *bool { return &value }
