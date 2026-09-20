package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
		{ID: "plan", Title: "Plan", Owner: "parent"},
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
	tasks := []db.TaskGraphTaskCreate{{ID: "work", Title: "Work", Owner: "parent"}}
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "First", tasks); err != nil {
		t.Fatalf("first CreateTaskGraph: %v", err)
	}
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "Second", tasks); err == nil {
		t.Fatal("second active task graph was accepted")
	}
}
