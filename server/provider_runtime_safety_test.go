package server

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

// runtimeSafetyService is a saved-route service that identifies the key it
// stands for and reports each request it serves.
type runtimeSafetyService struct {
	llm.Service
	key    string
	served chan<- string
}

func (s runtimeSafetyService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	select {
	case s.served <- s.key:
	default:
	}
	return s.Service.Do(ctx, req)
}

const runtimeSafetyModelID = "provider:c3JjLWE:Z3B0LWx1bmEtY3VzdG9t"

// runtimeSafetyEnv is a conversation whose warm, idle loop runs on a saved
// provider route.
type runtimeSafetyEnv struct {
	server *Server
	db     *db.DB
	models *models.Manager
	cm     *ConversationManager
	convID string
	served chan string
}

func (e *runtimeSafetyEnv) route(t *testing.T, key string) models.Built {
	t.Helper()
	built, err := models.BuildProviderModel("src-a", "gpt-luna-custom", runtimeSafetyModelID, "Work", "openai", "https://api.openai.com/v1", key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	built.Service = runtimeSafetyService{predictable.NewService(), key, e.served}
	return built
}

func newRuntimeSafetyEnv(t *testing.T) *runtimeSafetyEnv {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	e := &runtimeSafetyEnv{db: database, served: make(chan string, 16)}
	manager, err := models.NewManager(&models.Config{Models: []models.Built{e.route(t, "old-key")}})
	if err != nil {
		t.Fatal(err)
	}
	e.models = manager
	e.server = NewServer(database, manager, claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
		false, runtimeSafetyModelID, "")
	e.server.hooksDir = t.TempDir()
	model := runtimeSafetyModelID
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

	service, err := manager.GetService(runtimeSafetyModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cm.ensureLoop(service, runtimeSafetyModelID); err != nil {
		t.Fatalf("initial ensureLoop: %v", err)
	}
	e.cm.mu.Lock()
	warm := e.cm.loop != nil && !e.cm.agentWorking && e.cm.loopSourceID == "src-a"
	e.cm.mu.Unlock()
	if !warm {
		t.Fatal("fixture loop is not a warm idle saved-route loop")
	}
	return e
}

func (e *runtimeSafetyEnv) loop() any {
	e.cm.mu.Lock()
	defer e.cm.mu.Unlock()
	return e.cm.loop
}

func (e *runtimeSafetyEnv) queue(t *testing.T, id, text string) {
	t.Helper()
	if _, err := e.db.AppendQueuedMessage(t.Context(), e.convID, db.QueuedMessage{
		ID: id, Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"echo: ` + text + `"}]}`),
		CreatedAt: time.Now(), Model: runtimeSafetyModelID,
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *runtimeSafetyEnv) drain() { <-e.cm.drainPendingMessages(e.server) }

// firstServedBy waits for the first request any loop sends to a saved-route
// service and returns the key it carried.
func (e *runtimeSafetyEnv) firstServedBy(t *testing.T) string {
	t.Helper()
	select {
	case key := <-e.served:
		return key
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the provider route")
		return ""
	}
}

// A warm loop used to take queued messages directly, bypassing the saved
// route's revision/deletion check that a cold start performs. Delivery now
// reconciles the route before recording or dequeuing anything.
func TestWarmQueuedDeliveryReconcilesSavedRouteBeforeRecording(t *testing.T) {
	t.Run("unchanged route keeps its loop and delivers", func(t *testing.T) {
		t.Parallel()
		e := newRuntimeSafetyEnv(t)
		before := e.loop()
		e.queue(t, "q-same", "unchanged")
		e.drain()
		if e.loop() != before {
			t.Fatal("unchanged route rebuilt its warm loop")
		}
		if got := queuedMessages(t, e.db, e.convID); len(got) != 0 || !userMessageRowExists(t, e.db, e.convID, "unchanged") {
			t.Fatalf("queued = %+v, want delivered", got)
		}
		if key := e.firstServedBy(t); key != "old-key" {
			t.Fatalf("served by %q, want old-key", key)
		}
	})

	t.Run("rotated key rebuilds the loop before the message is recorded", func(t *testing.T) {
		t.Parallel()
		e := newRuntimeSafetyEnv(t)
		before := e.loop()
		e.models.ReplaceProviderModels("src-a", []models.Built{e.route(t, "new-key")})
		e.queue(t, "q-rotated", "after rotation")
		e.drain()
		if e.loop() == before {
			t.Fatal("rotated key kept the warm loop that holds the old credentials")
		}
		if got := queuedMessages(t, e.db, e.convID); len(got) != 0 || !userMessageRowExists(t, e.db, e.convID, "after rotation") {
			t.Fatalf("queued = %+v, want delivered", got)
		}
		if key := e.firstServedBy(t); key != "new-key" {
			t.Fatalf("queued turn used %q, want the rotated key", key)
		}
		e.cm.mu.Lock()
		revision := e.cm.loopSourceRevision
		e.cm.mu.Unlock()
		_, _, want, err := e.models.GetServiceSnapshot(runtimeSafetyModelID)
		if err != nil || revision != want {
			t.Fatalf("loop revision = %d, want %d, err=%v", revision, want, err)
		}
	})

	t.Run("deleted route leaves the queue intact and recovers when restored", func(t *testing.T) {
		t.Parallel()
		e := newRuntimeSafetyEnv(t)
		before := e.loop()
		e.models.ReplaceProviderModels("src-a", nil)
		e.queue(t, "q-deleted", "route deleted")
		e.drain()
		got := queuedMessages(t, e.db, e.convID)
		if len(got) != 1 || got[0].ID != "q-deleted" {
			t.Fatalf("queued = %+v, want the message retained", got)
		}
		if userMessageRowExists(t, e.db, e.convID, "route deleted") {
			t.Fatal("message was recorded although its route was deleted")
		}
		if e.loop() != before {
			t.Fatal("failed reconciliation replaced the loop")
		}
		select {
		case key := <-e.served:
			t.Fatalf("deleted route served a request with %q", key)
		default:
		}

		e.models.ReplaceProviderModels("src-a", []models.Built{e.route(t, "restored-key")})
		e.drain()
		if got := queuedMessages(t, e.db, e.convID); len(got) != 0 || !userMessageRowExists(t, e.db, e.convID, "route deleted") {
			t.Fatalf("queued after restore = %+v, want delivered", got)
		}
		if key := e.firstServedBy(t); key != "restored-key" {
			t.Fatalf("restored delivery used %q, want restored-key", key)
		}
	})

	t.Run("running turn is not interrupted by rotation or deletion", func(t *testing.T) {
		t.Parallel()
		e := newRuntimeSafetyEnv(t)
		before := e.loop()
		e.cm.mu.Lock()
		e.cm.agentWorking = true
		e.cm.mu.Unlock()
		e.models.ReplaceProviderModels("src-a", []models.Built{e.route(t, "new-key")})
		e.models.ReplaceProviderModels("src-a", nil)
		e.queue(t, "q-busy", "during turn")
		e.drain()
		if e.loop() != before {
			t.Fatal("an in-flight turn's loop was replaced")
		}
		if got := queuedMessages(t, e.db, e.convID); len(got) != 0 {
			t.Fatalf("queued = %+v, want delivered to the running loop", got)
		}
		if key := e.firstServedBy(t); key != "old-key" {
			t.Fatalf("running loop served %q, want its original key", key)
		}
	})
}

// Refreshing the first saved account without change must not hand "default
// model" to another account.
func TestNoOpProviderRefreshKeepsDefaultAccount(t *testing.T) {
	t.Parallel()
	route := func(source, id string) models.Built {
		return models.Built{ID: id, Provider: models.ProviderOpenAI, SourceID: source, Source: source,
			APIModelName: "gpt-luna-custom", Service: predictable.NewService()}
	}
	a, b := route("account-a", "provider:a:gpt"), route("account-b", "provider:b:gpt")
	manager, err := models.NewManager(&models.Config{})
	if err != nil {
		t.Fatal(err)
	}
	manager.ReplaceProviderModels("account-a", []models.Built{a})
	manager.ReplaceProviderModels("account-b", []models.Built{b})
	s := &Server{llmManager: manager, logger: slog.Default()}
	for _, step := range []string{"initial", "no-op refresh of first account", "refresh again"} {
		if step != "initial" {
			manager.ReplaceProviderModels("account-a", []models.Built{a})
		}
		if got := s.effectiveDefaultModel(s.getModelList()); got != a.ID {
			t.Fatalf("%s: default model = %q, want first account's %q", step, got, a.ID)
		}
	}
	// Deleting the first account does promote the next; that is intentional.
	manager.ReplaceProviderModels("account-a", nil)
	if got := s.effectiveDefaultModel(s.getModelList()); got != b.ID {
		t.Fatalf("after deleting first account default = %q, want %q", got, b.ID)
	}
}

func TestProviderCatalogChangesKeepUnchangedWarmLoop(t *testing.T) {
	e := newRuntimeSafetyEnv(t)
	build := func(id string) models.Built {
		t.Helper()
		built, err := models.BuildProviderModel("src-a", id, id, id, "openai", "https://api.openai.com/v1", "key", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		built.Service = e.route(t, "old-key").Service
		return built
	}
	current, sibling := build(runtimeSafetyModelID), build("provider:sibling")
	e.models.ReplaceProviderModels("src-a", []models.Built{current})
	if err := e.cm.ensureLoop(nil, runtimeSafetyModelID); err != nil {
		t.Fatal(err)
	}
	before := e.loop()
	for _, routes := range [][]models.Built{{current, sibling}, {current}} {
		e.models.ReplaceProviderModels("src-a", routes)
		if err := e.cm.ensureLoop(nil, runtimeSafetyModelID); err != nil {
			t.Fatal(err)
		}
		if e.loop() != before {
			t.Fatal("unrelated catalog change rebuilt the loop and its tools")
		}
	}
}
