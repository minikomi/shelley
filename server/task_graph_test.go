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

	empty := httptest.NewRecorder()
	server.handleListTaskGraphs(empty, httptest.NewRequest(http.MethodGet, "/", nil), parent.ConversationID)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty graph status = %d: %s", empty.Code, empty.Body.String())
	}
	var graphs []db.TaskGraphSnapshot
	if err := json.Unmarshal(empty.Body.Bytes(), &graphs); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if graphs == nil || len(graphs) != 0 {
		t.Fatalf("empty response = %#v, want []", graphs)
	}

	graph, err := database.CreateTaskGraph(t.Context(), parent.ConversationID, "Plan", "", 0, []db.TaskGraphTaskCreate{
		{ID: "plan", Title: "Plan", Prompt: "Plan"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	w := httptest.NewRecorder()
	server.handleListTaskGraphs(w, httptest.NewRequest(http.MethodGet, "/", nil), parent.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("graph status = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &graphs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(graphs) != 1 || graphs[0].GraphID != graph.GraphID {
		t.Fatalf("response graphs = %#v, want %q", graphs, graph.GraphID)
	}

	active := httptest.NewRecorder()
	server.handleListTaskGraphs(active, httptest.NewRequest(http.MethodGet, "/?active=1", nil), parent.ConversationID)
	graphs = nil
	if err := json.Unmarshal(active.Body.Bytes(), &graphs); err != nil {
		t.Fatalf("decode active response: %v", err)
	}
	if len(graphs) != 1 || graphs[0].GraphID != graph.GraphID {
		t.Fatalf("active graphs = %#v, want %q", graphs, graph.GraphID)
	}

	one := httptest.NewRecorder()
	server.handleListTaskGraphs(one, httptest.NewRequest(http.MethodGet, "/?graph_id="+graph.GraphID, nil), parent.ConversationID)
	graphs = nil
	if err := json.Unmarshal(one.Body.Bytes(), &graphs); err != nil {
		t.Fatalf("decode one graph response: %v", err)
	}
	if len(graphs) != 1 || graphs[0].GraphID != graph.GraphID {
		t.Fatalf("one graph = %#v, want %q", graphs, graph.GraphID)
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
	graph := &db.TaskGraphSnapshot{Context: "Follow the shared rules.", Tasks: []db.TaskGraphTask{
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
		"Follow the shared rules.",
		"Shared context, constraints, and acceptance criteria:",
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

func TestTaskGraphsAllowConcurrentActiveGraphs(t *testing.T) {
	server, database, _ := newTestServer(t)
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	tasks := []db.TaskGraphTaskCreate{{ID: "work", Title: "Work", Prompt: "Work"}}
	first, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "First", "", 0, tasks)
	if err != nil {
		t.Fatalf("first CreateTaskGraph: %v", err)
	}
	second, err := server.CreateTaskGraph(t.Context(), parent.ConversationID, "Second", "", 0, tasks)
	if err != nil {
		t.Fatalf("second CreateTaskGraph: %v", err)
	}
	latest, err := database.GetLatestTaskGraphSnapshot(t.Context(), parent.ConversationID)
	if err != nil {
		t.Fatalf("GetLatestTaskGraphSnapshot: %v", err)
	}
	if latest == nil || latest.GraphID != second.GraphID || latest.Status != "active" {
		t.Fatalf("latest graph = %#v, want active %q", latest, second.GraphID)
	}
	firstSnapshot, err := database.GetTaskGraphSnapshot(t.Context(), parent.ConversationID, first.GraphID)
	if err != nil {
		t.Fatalf("GetTaskGraphSnapshot: %v", err)
	}
	if firstSnapshot.Status != "active" {
		t.Fatalf("first graph status = %q, want active", firstSnapshot.Status)
	}
}

func TestCancelConversationCancelsActiveTaskGraphs(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	type activeTaskGraph struct {
		graph db.TaskGraphSnapshot
		child string
	}
	graphs := make([]activeTaskGraph, 0, 2)
	for _, id := range []string{"research", "implement"} {
		graph, err := database.CreateTaskGraph(ctx, parent.ConversationID, id, "", 0, []db.TaskGraphTaskCreate{
			{ID: id, Title: id, Prompt: id},
		})
		if err != nil {
			t.Fatalf("CreateTaskGraph(%q): %v", id, err)
		}
		claimed, err := database.ClaimReadyTaskGraphSubagentTasks(ctx, parent.ConversationID, graph.GraphID)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimReadyTaskGraphSubagentTasks(%q) = %d, %v; want one", id, len(claimed), err)
		}
		child, err := database.CreateSubagentConversation(ctx, id+"-child", parent.ConversationID, nil)
		if err != nil {
			t.Fatalf("CreateSubagentConversation(%q): %v", id, err)
		}
		if err := database.SetTaskGraphTaskChildConversation(ctx, parent.ConversationID, graph.GraphID, id, child.ConversationID, id+"-child"); err != nil {
			t.Fatalf("SetTaskGraphTaskChildConversation(%q): %v", id, err)
		}
		startSlowTurn(t, server, child.ConversationID)
		graphs = append(graphs, activeTaskGraph{graph: *graph, child: child.ConversationID})
	}

	cancelConversation(t, server, parent.ConversationID)

	for _, graph := range graphs {
		waitFor(t, 5*time.Second, func() bool {
			return !server.IsAgentWorking(graph.child)
		})
		snapshot, err := database.GetTaskGraphSnapshot(ctx, parent.ConversationID, graph.graph.GraphID)
		if err != nil {
			t.Fatalf("GetTaskGraphSnapshot: %v", err)
		}
		if snapshot.Status != "cancelled" || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Status != "cancelled" {
			t.Fatalf("graph after stop = %#v, want cancelled graph and task", snapshot)
		}
	}
}

func TestTaskGraphSnapshotReadsResultFromChild(t *testing.T) {
	server, database, _ := newTestServer(t)
	ctx := t.Context()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	graph, err := database.CreateTaskGraph(ctx, parent.ConversationID, "Research", "", 0, []db.TaskGraphTaskCreate{
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
