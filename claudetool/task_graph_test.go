package claudetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

type taskGraphServiceStub struct {
	created []db.TaskGraphTaskCreate
}

func (s *taskGraphServiceStub) CreateTaskGraph(_ context.Context, parent, title string, tasks []db.TaskGraphTaskCreate) (*db.TaskGraphSnapshot, error) {
	s.created = tasks
	return &db.TaskGraphSnapshot{GraphID: "graph", ParentConversationID: parent, Title: title}, nil
}
func (*taskGraphServiceStub) GetLatestTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (*taskGraphServiceStub) GetTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (*taskGraphServiceStub) CompleteTaskGraphTask(context.Context, string, string, string, string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (*taskGraphServiceStub) AwaitTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (*taskGraphServiceStub) CancelTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, []string, error) {
	return nil, nil, nil
}

func TestTaskGraphToolValidatesGraphAndReturnsSnapshot(t *testing.T) {
	stub := &taskGraphServiceStub{}
	tool := (&TaskGraphTool{Service: stub, ParentConversationID: "parent"}).Tool()
	input, err := json.Marshal(taskGraphInput{
		Action: "create",
		Title:  "Graph",
		Tasks: []taskGraphTask{
			{ID: "z-research", Title: "Research", Owner: "subagent", Prompt: "Research this", FileScopes: []string{"docs"}},
			{ID: "a-write", Title: "Write", Owner: "parent", Dependencies: []string{"z-research"}, FileScopes: []string{"docs"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := tool.Run(t.Context(), input)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	display, ok := out.Display.(*db.TaskGraphSnapshot)
	if !ok || display.GraphID != "graph" {
		t.Fatalf("display = %#v, want graph snapshot", out.Display)
	}
	if len(out.LLMContent) != 1 || !strings.Contains(out.LLMContent[0].Text, `"id":"graph"`) {
		t.Fatalf("model output = %#v, want graph ID", out.LLMContent)
	}
	if len(stub.created) != 2 {
		t.Fatalf("created %d tasks, want 2", len(stub.created))
	}
	if stub.created[0].ID != "z-research" || stub.created[1].ID != "a-write" {
		t.Fatalf("created order = %q, %q; want input order", stub.created[0].ID, stub.created[1].ID)
	}
}

func TestTaskGraphToolDescriptionRequiresGraphBeforeMultiScopeWork(t *testing.T) {
	description := (&TaskGraphTool{}).Tool().Description
	for _, required := range []string{
		"multiple substantial stages",
		"MUST call create before",
		"A prose plan is not a task graph",
		"delegation would not shorten the",
	} {
		if !strings.Contains(description, required) {
			t.Errorf("task graph description missing %q", required)
		}
	}
}

func TestTaskGraphToolRejectsInvalidConcurrentScopesAndCycles(t *testing.T) {
	tool := &TaskGraphTool{Service: &taskGraphServiceStub{}, ParentConversationID: "parent"}
	for _, tasks := range [][]taskGraphTask{
		{
			{ID: "one", Title: "One", Owner: "subagent", Prompt: "one", FileScopes: []string{"server"}},
			{ID: "two", Title: "Two", Owner: "subagent", Prompt: "two", FileScopes: []string{"server/api"}},
		},
		{
			{ID: "one", Title: "One", Owner: "parent", Dependencies: []string{"two"}},
			{ID: "two", Title: "Two", Owner: "parent", Dependencies: []string{"one"}},
		},
		{
			{ID: "one", Title: "One", Owner: "subagent"},
		},
	} {
		if _, err := tool.validateCreate(taskGraphInput{Action: "create", Title: "Graph", Tasks: tasks}); err == nil {
			t.Fatalf("validateCreate(%+v) succeeded, want error", tasks)
		}
	}
}

func TestTaskGraphToolUnavailableToNestedSubagents(t *testing.T) {
	stub := &taskGraphServiceStub{}
	topLevel := NewToolSet(t.Context(), ToolSetConfig{
		ParentConversationID: "parent",
		TaskGraphService:     stub,
	})
	defer topLevel.Cleanup()
	if !hasTaskGraphTool(topLevel.Tools()) {
		t.Fatal("top-level tool set does not include task_graph")
	}

	nested := NewToolSet(t.Context(), ToolSetConfig{
		ParentConversationID: "parent",
		TaskGraphService:     stub,
		SubagentDepth:        1,
	})
	defer nested.Cleanup()
	if hasTaskGraphTool(nested.Tools()) {
		t.Fatal("nested subagent tool set includes task_graph")
	}
}

func hasTaskGraphTool(tools []*llm.Tool) bool {
	for _, tool := range tools {
		if tool.Name == taskGraphName {
			return true
		}
	}
	return false
}
