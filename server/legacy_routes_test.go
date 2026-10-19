package server

// Legacy-prefixed tests: saved-provider support must not change the contract
// of routes that predate it (built-in and custom models).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/modelproviders"
	"shelley.exe.dev/models"
)

// POST /api/models/refresh keeps its original contract with a saved provider
// connected: a bare model array, no discovery of saved sources, and the
// original statuses and error text for refresh failures.
func TestLegacyModelRefreshIgnoresSavedProviders(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var discoveries atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoveries.Add(1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer fixture.Close()
	llmManager, err := models.NewManager(&models.Config{
		Models: []models.Built{{ID: "old-built", Provider: models.ProviderBuiltIn, Source: "old", Service: predictable.NewService()}},
		DB:     database,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database, llmManager: llmManager, logger: slog.New(slog.DiscardHandler)}
	s.SetModelProviderManager(modelproviders.New(database, llmManager, fixture.Client()))
	if _, err := s.modelProviders.Create(t.Context(), modelproviders.Input{
		ProviderType: "openai", Endpoint: fixture.URL + "/v1", APIKey: "secret", DisplayName: "Fixture",
	}); err != nil {
		t.Fatal(err)
	}
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleModelRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/models/refresh", nil))
		return rec
	}

	// Not configured: the original 501, even though saved providers exist.
	discoveries.Store(0)
	if rec := post(); rec.Code != http.StatusNotImplemented || rec.Body.String() != "model refresh is not configured\n" {
		t.Fatalf("unconfigured = %d %q, want baseline 501", rec.Code, rec.Body.String())
	}

	// Builder failure: the original 500 with the bare error text.
	s.refreshBuiltModels = func(context.Context) ([]models.Built, error) { return nil, fmt.Errorf("catalog unavailable") }
	if rec := post(); rec.Code != http.StatusInternalServerError || rec.Body.String() != "catalog unavailable\n" {
		t.Fatalf("builder failure = %d %q, want baseline 500", rec.Code, rec.Body.String())
	}

	// Success: a bare array of models, with no saved-source envelope.
	s.refreshBuiltModels = func(context.Context) ([]models.Built, error) {
		return []models.Built{{ID: "new-built", Provider: models.ProviderBuiltIn, Source: "new", Service: predictable.NewService()}}, nil
	}
	rec := post()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "[") {
		t.Fatalf("success = %d %q, want 200 with a bare array", rec.Code, rec.Body.String())
	}
	var got []ModelInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a model array: %v: %s", err, rec.Body.String())
	}
	if llmManager.HasModel("old-built") || !llmManager.HasModel("new-built") {
		t.Fatalf("built models were not refreshed: %v", llmManager.GetAvailableModels())
	}
	if n := discoveries.Load(); n != 0 {
		t.Fatalf("legacy refresh discovered saved sources %d times, want 0", n)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("credential leaked: %s", rec.Body.String())
	}
}

// Without a saved-provider catalog, a unified-stream connection carries no
// models_changed frame: none with no manager, none with a manager that has no
// source state. A marker broadcast proves the initial frames were already sent.
func TestLegacyStreamOmitsCatalogFrameWithoutProviderState(t *testing.T) {
	for _, name := range []string{"no manager", "manager without state"} {
		t.Run(name, func(t *testing.T) {
			svr, database, _ := newTestServer(t)
			if name == "manager without state" {
				svr.SetModelProviderManager(modelproviders.New(database, nopPublisher{}, nil))
			}
			mux := http.NewServeMux()
			svr.RegisterRoutes(mux)
			httpServer := httptest.NewServer(mux)
			t.Cleanup(httpServer.Close)
			if err := svr.conversationListStream.recompute(t.Context()); err != nil {
				t.Fatal(err)
			}

			scanner, _ := openDiskSpaceStream(t, httpServer.URL+"/api/stream2?conversation_list_hash="+svr.conversationListStream.currentHash)
			readHeartbeat := func(conversationID string) bool {
				for scanner.Scan() {
					line := scanner.Text()
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var frame StreamResponse
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
						t.Fatal(err)
					}
					if frame.ModelsChanged {
						t.Fatalf("unexpected models_changed frame: %+v", frame)
					}
					if frame.Heartbeat && frame.ConversationID == conversationID {
						return true
					}
				}
				return false
			}
			// The first heartbeat commits the stream; a marker published
			// afterwards is ordered after every initial frame.
			if !readHeartbeat("") {
				t.Fatalf("initial heartbeat not received: %v", scanner.Err())
			}
			svr.streamPub.Broadcast(StreamResponse{Heartbeat: true, ConversationID: "marker"})
			if !readHeartbeat("marker") {
				t.Fatalf("marker not received: %v", scanner.Err())
			}
		})
	}
}

// legacyWarmEnv is a conversation whose warm, idle loop runs on a built-in
// (non-saved) route.
type legacyWarmEnv struct {
	server *Server
	db     *db.DB
	models *models.Manager
	cm     *ConversationManager
	convID string
	served chan string
}

const legacyWarmModelID = "legacy-warm"

func newLegacyWarmEnv(t *testing.T, seedRetryableError bool) *legacyWarmEnv {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	e := &legacyWarmEnv{db: database, served: make(chan string, 16)}
	manager, err := models.NewManager(&models.Config{Models: []models.Built{{
		ID: legacyWarmModelID, Provider: models.ProviderBuiltIn, Source: "builtin",
		Service: runtimeSafetyService{predictable.NewService(), "legacy-key", e.served},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	e.models = manager
	e.server = NewServer(database, manager, claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
		false, legacyWarmModelID, "")
	e.server.hooksDir = t.TempDir()
	model := legacyWarmModelID
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.convID = conversation.ConversationID
	e.cm, err = e.server.getOrCreateConversationManager(t.Context(), e.convID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopActiveConversationLoops(e.server) })
	if seedRetryableError {
		// Seeded before the loop exists, so its history includes the request.
		e.retryableError(t)
	}
	service, err := manager.GetService(legacyWarmModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cm.ensureLoop(service, legacyWarmModelID); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *legacyWarmEnv) loop() any {
	e.cm.mu.Lock()
	defer e.cm.mu.Unlock()
	return e.cm.loop
}

func (e *legacyWarmEnv) retryableError(t *testing.T) {
	t.Helper()
	if _, err := e.db.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: e.convID, Type: db.MessageTypeUser, LLMData: llm.UserStringMessage("echo: retry legacy"),
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
}

// A warm legacy loop is retried as it is: Retry neither re-resolves its model
// nor rebuilds its loop, so it still works when the catalog no longer lists the
// model (only saved routes re-resolve).
func TestLegacyRetryWarmLoopDoesNotReresolve(t *testing.T) {
	e := newLegacyWarmEnv(t, true)
	before := e.loop()
	if before == nil {
		t.Fatal("fixture has no warm loop")
	}
	if err := e.models.RefreshBuiltModels([]models.Built{{
		ID: "other-built", Provider: models.ProviderBuiltIn, Source: "builtin", Service: predictable.NewService(),
	}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.server.handleRetryConversation(rec,
		httptest.NewRequest(http.MethodPost, "/api/conversation/"+e.convID+"/retry", nil), e.convID)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"retrying"`) {
		t.Fatalf("retry status=%d body=%s, want baseline 202 retrying", rec.Code, rec.Body.String())
	}
	if e.loop() != before {
		t.Fatal("warm legacy retry rebuilt its loop")
	}
	select {
	case key := <-e.served:
		if key != "legacy-key" {
			t.Fatalf("retry served by %q", key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not reach the warm loop's service")
	}
}

// A legacy model never re-resolves, so a resolver failure for it (or a resolver
// that disagrees with the caller's service) leaves its warm loop and service
// alone; only saved routes consult the resolver result.
func TestLegacyEnsureLoopIgnoresSavedRouteResolver(t *testing.T) {
	e := newLegacyWarmEnv(t, false)
	before := e.loop()
	e.cm.resolveModelService = func(string) (llm.Service, string, uint64, error) {
		t.Error("legacy loop must not consult the saved-provider resolver")
		return nil, "", 0, fmt.Errorf("resolver unavailable")
	}
	service, err := e.models.GetService(legacyWarmModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cm.ensureLoop(service, legacyWarmModelID); err != nil {
		t.Fatalf("legacy ensureLoop failed on resolver error: %v", err)
	}
	if e.loop() != before {
		t.Fatal("legacy loop was rebuilt")
	}
}

// Continue with an omitted model keeps its original meaning: the built-in
// default model (models.Default()), not the first ready model, which may be a
// saved route. When that default is not ready the request is rejected.
func TestLegacyContinueOmittedModelUsesBuiltInDefault(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	manager, err := models.NewManager(&models.Config{})
	if err != nil {
		t.Fatal(err)
	}
	const savedID = "provider:c2F2ZWQ:bHVuYQ"
	manager.ReplaceProviderModels("saved", []models.Built{{
		ID: savedID, SourceID: "saved", Source: "Saved account",
		Provider: models.ProviderOpenAI, APIModelName: "gpt-6-luna", Service: predictable.NewService(),
	}})
	svr := NewServer(database, manager, claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
		false, savedID, "")
	svr.hooksDir = t.TempDir()
	model := savedID
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: conversation.ConversationID, Type: db.MessageTypeError,
		LLMData: llm.Message{
			Role: llm.MessageRoleAssistant, EndOfTurn: true,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "refused"}},
		},
		UserData: map[string]any{"error_type": string(llm.ErrorTypeRefusal)},
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	svr.handleContinueConversation(rec, httptest.NewRequest(http.MethodPost,
		"/api/conversation/"+conversation.ConversationID+"/continue", nil), conversation.ConversationID)
	want := "Unknown or unavailable model: " + models.Default().ID + "\n"
	if rec.Code != http.StatusBadRequest || rec.Body.String() != want {
		t.Fatalf("continue = %d %q, want 400 %q", rec.Code, rec.Body.String(), want)
	}
}
