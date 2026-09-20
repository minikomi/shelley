package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

func TestGetTaskGraph(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	notFound := httptest.NewRecorder()
	server.handleGetTaskGraph(notFound, httptest.NewRequest(http.MethodGet, "/", nil), parent.ConversationID)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("empty graph status = %d, want 404", notFound.Code)
	}

	graph, err := database.CreateTaskGraph(t.Context(), parent.ConversationID, "Plan", []db.TaskGraphTaskCreate{
		{ID: "plan", Title: "Plan", Prompt: "Plan"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	w := httptest.NewRecorder()
	server.handleGetTaskGraph(w, httptest.NewRequest(http.MethodGet, "/", nil), parent.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("graph status = %d: %s", w.Code, w.Body.String())
	}
	var body db.TaskGraphSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.GraphID != graph.GraphID {
		t.Fatalf("response graph = %#v, want %q", body, graph.GraphID)
	}
}

func TestTaskGraphAwaitedTreatsFailuresAsTerminal(t *testing.T) {
	graph := &db.TaskGraphSnapshot{
		Status: "failed",
		Tasks: []db.TaskGraphTask{
			{ID: "failed", Status: "failed"},
		},
	}
	if !taskGraphAwaited(graph, nil) {
		t.Fatal("failed graph did not satisfy whole-graph await")
	}
	if !taskGraphAwaited(graph, []string{"failed"}) {
		t.Fatal("failed task did not satisfy task await")
	}
}

func TestTaskGraphChangeWakesAllWaiters(t *testing.T) {
	server, _, _ := newTestServer(t)
	first := server.taskGraphWaitChannel()
	second := server.taskGraphWaitChannel()
	server.signalTaskGraphChange()
	for name, waiter := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-waiter:
		default:
			t.Fatalf("%s waiter was not woken", name)
		}
	}
}

func TestTaskGraphRejectsSecondActiveGraph(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	tasks := []db.TaskGraphTaskCreate{{ID: "work", Title: "Work", Prompt: "Work"}}
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "First", tasks); err != nil {
		t.Fatalf("first CreateTaskGraph: %v", err)
	}
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "Second", tasks); err == nil {
		t.Fatal("second active task graph was accepted")
	}
}

func TestCancelConversationCancelsActiveTaskGraph(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	graph, err := database.CreateTaskGraph(ctx, parent.ConversationID, "Research", []db.TaskGraphTaskCreate{
		{ID: "research", Title: "Research", Prompt: "Research"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(ctx, graph.GraphID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks = %d, %v; want one", len(claimed), err)
	}
	child, err := database.CreateSubagentConversation(ctx, "research-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatalf("CreateSubagentConversation: %v", err)
	}
	if err := database.SetTaskGraphTaskChildConversation(ctx, graph.GraphID, "research", child.ConversationID, "research-child"); err != nil {
		t.Fatalf("SetTaskGraphTaskChildConversation: %v", err)
	}
	startSlowTurn(t, server, child.ConversationID)

	cancelConversation(t, server, parent.ConversationID)

	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(child.ConversationID)
	})
	snapshot, err := database.GetTaskGraphSnapshot(ctx, graph.GraphID)
	if err != nil {
		t.Fatalf("GetTaskGraphSnapshot: %v", err)
	}
	if snapshot.Status != "cancelled" || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Status != "cancelled" {
		t.Fatalf("graph after stop = %#v, want cancelled graph and task", snapshot)
	}
}
