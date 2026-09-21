package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
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

	graph, err := database.CreateTaskGraph(t.Context(), parent.ConversationID, "Plan", 0, []db.TaskGraphTaskCreate{
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

func TestTaskGraphTaskPromptIncludesDirectDependencyResults(t *testing.T) {
	graph := &db.TaskGraphSnapshot{Tasks: []db.TaskGraphTask{
		{ID: "research", Title: "Research API", Status: "complete", Result: "Use endpoint /v2."},
		{ID: "schema", Title: "Define schema", Status: "complete"},
		{ID: "unrelated", Title: "Unrelated", Status: "complete", Result: "Do not include me."},
	}}
	task := db.TaskGraphTask{
		ID:           "implement",
		Prompt:       "Implement the client.",
		Dependencies: []string{"research", "schema"},
	}

	got := taskGraphTaskPrompt(graph, task)
	for _, want := range []string{
		"Implement the client.",
		"Completed direct dependency results",
		"dependency research: Research API",
		"Use endpoint /v2.",
		"dependency schema: Define schema",
		"(completed without a final response)",
		"verify claims against the shared working tree",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Do not include me.") {
		t.Fatalf("prompt includes non-dependency result:\n%s", got)
	}
}

func TestTaskGraphTaskPromptLeavesRootPromptUnchanged(t *testing.T) {
	task := db.TaskGraphTask{ID: "root", Prompt: "Do the work."}
	if got := taskGraphTaskPrompt(&db.TaskGraphSnapshot{}, task); got != task.Prompt {
		t.Fatalf("root prompt = %q, want %q", got, task.Prompt)
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
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "First", 0, tasks); err != nil {
		t.Fatalf("first CreateTaskGraph: %v", err)
	}
	if _, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "Second", 0, tasks); err == nil {
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
	graph, err := database.CreateTaskGraph(ctx, parent.ConversationID, "Research", 0, []db.TaskGraphTaskCreate{
		{ID: "research", Title: "Research", Prompt: "Research"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(ctx, parent.ConversationID, graph.GraphID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks = %d, %v; want one", len(claimed), err)
	}
	child, err := database.CreateSubagentConversation(ctx, "research-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatalf("CreateSubagentConversation: %v", err)
	}
	if err := database.SetTaskGraphTaskChildConversation(ctx, parent.ConversationID, graph.GraphID, "research", child.ConversationID, "research-child"); err != nil {
		t.Fatalf("SetTaskGraphTaskChildConversation: %v", err)
	}
	startSlowTurn(t, server, child.ConversationID)

	cancelConversation(t, server, parent.ConversationID)

	waitFor(t, 5*time.Second, func() bool {
		return !server.IsAgentWorking(child.ConversationID)
	})
	snapshot, err := database.GetTaskGraphSnapshot(ctx, parent.ConversationID, graph.GraphID)
	if err != nil {
		t.Fatalf("GetTaskGraphSnapshot: %v", err)
	}
	if snapshot.Status != "cancelled" || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Status != "cancelled" {
		t.Fatalf("graph after stop = %#v, want cancelled graph and task", snapshot)
	}
}

func TestTaskGraphSnapshotReadsResultFromChild(t *testing.T) {
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	graph, err := database.CreateTaskGraph(ctx, parent.ConversationID, "Research", 0, []db.TaskGraphTaskCreate{
		{ID: "research", Title: "Research", Prompt: "Research"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	if _, err := database.ClaimReadyTaskGraphSubagentTasks(ctx, parent.ConversationID, graph.GraphID); err != nil {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks: %v", err)
	}
	child, err := database.CreateSubagentConversation(ctx, "research-child", parent.ConversationID, nil)
	if err != nil {
		t.Fatalf("CreateSubagentConversation: %v", err)
	}
	if err := database.SetTaskGraphTaskChildConversation(ctx, parent.ConversationID, graph.GraphID, "research", child.ConversationID, "research-child"); err != nil {
		t.Fatalf("SetTaskGraphTaskChildConversation: %v", err)
	}
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: child.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        llm.Message{Role: llm.MessageRoleAssistant, Content: llm.TextContent("Use endpoint /v2.")},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if _, _, err := database.CompleteTaskGraphChild(ctx, child.ConversationID); err != nil {
		t.Fatalf("CompleteTaskGraphChild: %v", err)
	}

	snapshot, err := server.GetTaskGraphSnapshot(ctx, parent.ConversationID, graph.GraphID)
	if err != nil {
		t.Fatalf("GetTaskGraphSnapshot: %v", err)
	}
	if snapshot.Tasks[0].Result != "Use endpoint /v2." {
		t.Fatalf("task result = %q, want child response", snapshot.Tasks[0].Result)
	}
	stored, err := database.GetConversationByID(ctx, parent.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	if strings.Contains(stored.ConversationOptions, "/v2") {
		t.Fatalf("result was persisted in conversation options: %s", stored.ConversationOptions)
	}
}
