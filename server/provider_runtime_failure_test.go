package server

// Failure-injection regressions for conversation account identity. Synthetic
// services and synchronous drains/handlers only; no sleeps or network.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/modelproviders"
	"shelley.exe.dev/models"
)

// failNextProviderRebuild advances the source revision of an existing warm
// idle loop, then fails only its replacement construction. The old loop has
// already been detached when serviceForLoop invokes this decoration hook.
func failNextProviderRebuild(t *testing.T, e *runtimeSafetyEnv) {
	t.Helper()
	injected := errors.New("replacement decoration failed once")
	calls := 0
	e.cm.decorateService = func(service llm.Service) (llm.Service, error) {
		calls++
		if calls == 1 {
			return nil, injected
		}
		return service, nil
	}
	e.models.ReplaceProviderModels("src-a", []models.Built{e.route(t, "new-key")})
	service, err := e.models.GetService(runtimeSafetyModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cm.ensureLoop(service, runtimeSafetyModelID); !errors.Is(err, injected) {
		t.Fatalf("replacement error = %v, want injected error", err)
	}
	// No loop remains to use this hook, and the next operation is synchronous.
	e.cm.decorateService = nil
	e.cm.mu.Lock()
	model, hydrated, hasLoop := e.cm.modelID, e.cm.hydrated, e.cm.loop != nil
	e.cm.mu.Unlock()
	if calls != 1 || hasLoop {
		t.Fatalf("fixture: decoration calls=%d hasLoop=%v", calls, hasLoop)
	}
	t.Logf("after failed rebuild: model=%q hydrated=%v hasLoop=%v", model, hydrated, hasLoop)
	if hydrated && model == "" {
		// Keep going: the following operation demonstrates the consequence.
		t.Error("failed rebuild left a hydrated manager with no conversation model")
	}
}

func TestFailedProviderRebuildKeepsQueuedRouteAccount(t *testing.T) {
	e := newRuntimeSafetyEnv(t)
	failNextProviderRebuild(t, e)
	// Subagent/background replies need not carry a model: the conversation's
	// persisted model must remain authoritative after a failed loop rebuild.
	if _, err := e.db.AppendQueuedMessage(t.Context(), e.convID, db.QueuedMessage{
		ID:        "fixture-model-less-reply",
		Llm:       []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: queued reply"}]}`),
		CreatedAt: time.Date(2026, time.October, 18, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	e.drain() // waits for the drain owner; no polling or sleeping
	if queued := queuedMessages(t, e.db, e.convID); len(queued) != 0 {
		t.Fatalf("healthy route could not drain after rebuild failure: queued=%+v model=%q", queued, e.cm.GetModel())
	}
	if key := e.firstServedBy(t); key != "new-key" {
		t.Fatalf("queued reply served by %q, want original account's rotated key", key)
	}
}

func TestFailedProviderRebuildKeepsRetryRouteAccount(t *testing.T) {
	e := newRuntimeSafetyEnv(t)
	const otherID = "provider:fixture-other:default"
	e.models.ReplaceProviderModels("fixture-other", []models.Built{{
		ID: otherID, SourceID: "fixture-other", Source: "Other account",
		Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna",
		Service: runtimeSafetyService{predictable.NewService(), "wrong-default-key", e.served},
	}})
	e.server.defaultModel = otherID
	if _, err := e.db.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: e.convID, Type: db.MessageTypeUser,
		LLMData: llm.UserStringMessage("echo: retry original account"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: e.convID, Type: db.MessageTypeError,
		LLMData: llm.Message{
			Role: llm.MessageRoleAssistant, EndOfTurn: true,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "retryable fixture error"}},
		},
		UserData: map[string]any{"retryable": true},
	}); err != nil {
		t.Fatal(err)
	}
	failNextProviderRebuild(t, e)
	rec := httptest.NewRecorder()
	e.server.handleRetryConversation(rec,
		httptest.NewRequest(http.MethodPost, "/api/conversation/"+e.convID+"/retry", nil), e.convID)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"retrying"`) {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	if model := e.cm.GetModel(); model != runtimeSafetyModelID {
		t.Errorf("retry rebound runtime model to %q, want saved conversation route %q", model, runtimeSafetyModelID)
	}
	if key := e.firstServedBy(t); key != "new-key" {
		t.Errorf("retry actually reached %q, want original account's rotated key", key)
	}
	stored, err := e.db.GetConversationByID(t.Context(), e.convID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model == nil || *stored.Model != runtimeSafetyModelID {
		t.Fatalf("stored conversation model changed: %+v", stored.Model)
	}
}

func TestBuiltRefreshKeepsMixedSourceDefault(t *testing.T) {
	for _, customFirst := range []bool{true, false} {
		name := "saved-before-new-custom"
		if customFirst {
			name = "startup-custom-before-saved"
		}
		t.Run(name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			t.Cleanup(cleanup)
			const customID = "fixture-individual-custom"
			createCustom := func() {
				t.Helper()
				if _, err := database.CreateModel(t.Context(), generated.CreateModelParams{
					ModelID: customID, DisplayName: "Individual custom account",
					ProviderType: "openai", Endpoint: "https://unused.invalid/v1",
					ApiKey: "synthetic-not-used", ModelName: "gpt-6-luna",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if customFirst {
				createCustom()
			}
			built := []models.Built{{
				ID: "predictable", Provider: models.ProviderBuiltIn,
				Source: "builtin", Service: predictable.NewService(),
			}}
			manager, err := models.NewManager(&models.Config{Models: built, DB: database})
			if err != nil {
				t.Fatal(err)
			}
			const savedID = "provider:fixture-saved:luna"
			manager.ReplaceProviderModels("fixture-saved", []models.Built{{
				ID: savedID, SourceID: "fixture-saved", Source: "Saved account",
				Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna",
				Service: predictable.NewService(),
			}})
			if !customFirst {
				createCustom()
				if err := manager.RefreshCustomModels(); err != nil {
					t.Fatal(err)
				}
			}
			s := &Server{llmManager: manager}
			want := savedID
			if customFirst {
				want = customID
			}
			if got := s.effectiveDefaultModel(s.getModelList()); got != want {
				t.Fatalf("fixture default=%q, want %q", got, want)
			}
			before := manager.GetAvailableModels()
			if err := manager.RefreshBuiltModels(built); err != nil {
				t.Fatal(err)
			}
			if got := s.effectiveDefaultModel(s.getModelList()); got != want {
				t.Fatalf("no-op built refresh changed default %q -> %q; order %v -> %v", want, got, before, manager.GetAvailableModels())
			}
		})
	}
}

// A conversation whose saved route is gone must say so; it must not be
// retried on the host default account.
func TestRetryOfDeletedSavedRouteDoesNotUseDefault(t *testing.T) {
	e := newRuntimeSafetyEnv(t)
	const otherID = "provider:fixture-other:default"
	e.models.ReplaceProviderModels("fixture-other", []models.Built{{
		ID: otherID, SourceID: "fixture-other", Source: "Other account",
		Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna",
		Service: runtimeSafetyService{predictable.NewService(), "wrong-default-key", e.served},
	}})
	e.server.defaultModel = otherID
	if _, err := e.db.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: e.convID, Type: db.MessageTypeError,
		LLMData: llm.Message{
			Role: llm.MessageRoleAssistant, EndOfTurn: true,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "retryable fixture error"}},
		},
		UserData: map[string]any{"retryable": true},
	}); err != nil {
		t.Fatal(err)
	}
	e.models.ReplaceProviderModels("src-a", nil)
	rec := httptest.NewRecorder()
	e.server.handleRetryConversation(rec,
		httptest.NewRequest(http.MethodPost, "/api/conversation/"+e.convID+"/retry", nil), e.convID)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), runtimeSafetyModelID) ||
		!strings.Contains(rec.Body.String(), "gpt-luna-custom") {
		t.Fatalf("retry status=%d body=%q, want 400 naming the saved route", rec.Code, rec.Body.String())
	}
	select {
	case key := <-e.served:
		t.Fatalf("retry reached %q instead of failing", key)
	default:
	}
}

func TestUnsupportedSavedProviderRouteMessageKeepsFullID(t *testing.T) {
	const id = runtimeSafetyModelID
	list := []ModelInfo{{ID: "other", Ready: true}, {ID: "provider:c3JjLWE:b3RoZXI", SourceID: "src-a", Source: "Work", Ready: true}}
	got := unsupportedModelMessageIn(id, list, false)
	for _, want := range []string{`"gpt-luna-custom"`, "Work", id} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q missing %q", got, want)
		}
	}
	if got := unsupportedModelMessageIn(id, []ModelInfo{{ID: "other", Ready: true}}, false); !strings.Contains(got, "its saved provider") || !strings.Contains(got, id) {
		t.Errorf("message without known account = %q", got)
	}
	if got := unsupportedModelMessageIn("provider:not-base64!:x", list, false); got != "Unsupported model: provider:not-base64!:x" {
		t.Errorf("malformed route message = %q", got)
	}
}

// Refreshing all saved connections reports a failed account as source state
// (sanitized: never credentials or upstream bodies), and a failure of the
// manager's own database read is a 500 JSON error.
func TestModelProviderRefreshAllSanitizesFailuresAndMapsDatabaseErrorTo500(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var failDiscovery atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if failDiscovery.Load() {
			http.Error(w, "upstream-secret-response", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer fixture.Close()
	llmManager, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	providers := modelproviders.New(database, llmManager, fixture.Client())
	s := &Server{db: database, llmManager: llmManager, logger: slog.New(slog.DiscardHandler)}
	s.SetModelProviderManager(providers)
	if _, err := providers.Create(t.Context(), modelproviders.Input{
		ProviderType: "openai", Endpoint: fixture.URL + "/v1", APIKey: "provider-secret", DisplayName: "Work account",
	}); err != nil {
		t.Fatal(err)
	}

	failDiscovery.Store(true)
	rec := httptest.NewRecorder()
	s.handleRefreshModelProviders(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers/refresh", nil))
	var sources []modelproviders.Connection
	if err := json.NewDecoder(rec.Body).Decode(&sources); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(sources) != 1 || sources[0].State != "error" || sources[0].DisplayName != "Work account" ||
		strings.Contains(sources[0].Error, "provider-secret") || strings.Contains(sources[0].Error, "upstream-secret-response") {
		t.Fatalf("status=%d sources=%+v, want 200 with one sanitized error-state connection", rec.Code, sources)
	}

	// A canceled request makes the manager's own database read fail.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec = httptest.NewRecorder()
	s.handleRefreshModelProviders(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers/refresh", nil).WithContext(ctx))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("database failure status = %d body %q, want 500", rec.Code, rec.Body.String())
	}
}
