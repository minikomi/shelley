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
	created        []db.TaskGraphTaskCreate
	awaitSnapshot  *db.TaskGraphSnapshot
	maxConcurrency int
}

func (s *taskGraphServiceStub) CreateTaskGraph(_ context.Context, parent, title string, maxConcurrency int, tasks []db.TaskGraphTaskCreate) (*db.TaskGraphSnapshot, error) {
	s.created = tasks
	s.maxConcurrency = maxConcurrency
	return &db.TaskGraphSnapshot{GraphID: "graph", ParentConversationID: parent, Title: title, MaxConcurrency: maxConcurrency}, nil
}
func (*taskGraphServiceStub) GetLatestTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (*taskGraphServiceStub) GetTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error) {
	return nil, nil
}
func (s *taskGraphServiceStub) AwaitTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, error) {
	return s.awaitSnapshot, nil
}
func (*taskGraphServiceStub) CancelTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, []string, error) {
	return nil, nil, nil
}

func TestTaskGraphToolValidatesGraphAndReturnsSnapshot(t *testing.T) {
	stub := &taskGraphServiceStub{}
	tool := (&TaskGraphTool{Service: stub, ParentConversationID: "parent"}).Tool()
	input, err := json.Marshal(taskGraphInput{
		Action:         "create",
		Title:          "Graph",
		MaxConcurrency: 5,
		Tasks: []taskGraphTask{
			{ID: "z-research", Title: "Research", Prompt: "Research this", FileScopes: []string{"docs/research"}},
			{ID: "a-design", Title: "Design", Prompt: "Design this", FileScopes: []string{"docs/design"}},
			{ID: "m-review", Title: "Review", Prompt: "Review the work", Dependencies: []string{"z-research", "a-design"}, FileScopes: []string{"docs"}},
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
	if len(stub.created) != 3 {
		t.Fatalf("created %d tasks, want 3", len(stub.created))
	}
	if stub.maxConcurrency != 5 || display.MaxConcurrency != 5 {
		t.Fatalf("max concurrency stub=%d display=%d, want 5", stub.maxConcurrency, display.MaxConcurrency)
	}
	if stub.created[0].ID != "z-research" || stub.created[1].ID != "a-design" || stub.created[2].ID != "m-review" {
		t.Fatalf("created order = %q, %q, %q; want input order", stub.created[0].ID, stub.created[1].ID, stub.created[2].ID)
	}
}

func TestTaskGraphToolDescriptionRequiresGraphBeforeMultiScopeWork(t *testing.T) {
	description := (&TaskGraphTool{}).Tool().Description
	for _, required := range []string{
		"multiple substantial stages",
		"MUST call create before",
		"bounded read-only shell pass",
		"guessed audit tasks",
		"A prose plan is not a task graph",
		"delegation would not shorten the",
		"concrete contract, acceptance criteria, and",
		"cheap bounded inspection needed to",
		"delegate implementation directly",
		"do not fan out discovery or research subagents",
		"independent unknowns genuinely block",
		"parent cannot resolve them cheaply",
		"implementation owner includes focused tests",
		"separate test-mapping or review filler tasks",
		"parent owns integration and final validation",
		"cohesive, independently testable outcomes",
		"file type",
		"substantial enough to amortize",
		"tightly coupled work",
		"bounded context packet",
		"owned scope, stable interfaces",
		"source-of-truth files, non-goals",
		"least costly capable model",
		"routine bounded work",
		"delegated subagent runs only",
		"parent planning",
		"completed direct",
		"handoff context",
		"shared files remain the source of truth",
		"must read, use, or coordinate",
		"immutable interface",
		"partially written files",
		"deliverable, validation performed",
		"remaining uncertainty",
		"parent integration needs",
		"cheap path first",
		"git diff --stat",
		"one aggregate acceptance command",
		"repeat focused checks",
		"substantial scope in the parent",
		"max_concurrency controls",
		"resource and cost constraints",
		"Branch whenever two useful subagent scopes",
		"serial_rationale",
		"Parent-owned work is not a reason",
		"create symmetry",
		"true blockers",
		"once per delegation wave",
		"call create again",
		"do not hardcode phase",
		"do not mutate a finished graph",
		"completed waves are immutable",
		"not research-only advisors",
		"implementation and modify files",
		"delegate those file-writing scopes",
		"establish any shared manifests",
		"shared configuration, interfaces",
		"Await or cancel the affected tasks first",
	} {
		if !strings.Contains(description, required) {
			t.Errorf("task graph description missing %q", required)
		}
	}
}

func TestTaskGraphToolTerminalAwaitPromptsAnotherDelegationDecision(t *testing.T) {
	stub := &taskGraphServiceStub{awaitSnapshot: &db.TaskGraphSnapshot{
		GraphID: "graph",
		Status:  "complete",
	}}
	tool := (&TaskGraphTool{Service: stub, ParentConversationID: "parent"}).Tool()
	input, err := json.Marshal(taskGraphInput{Action: "await", GraphID: "graph"})
	if err != nil {
		t.Fatal(err)
	}
	out := tool.Run(t.Context(), input)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	text := out.LLMContent[0].Text
	for _, required := range []string{
		"delegation wave is terminal",
		"cheap path first",
		"git diff --stat",
		"one aggregate acceptance command",
		"Do not repeat passing focused checks",
		"substantially rewrite child-owned work",
		"create a new graph first",
		"exclusive file scopes",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("terminal await output missing %q: %s", required, text)
		}
	}
}

func TestTaskGraphToolRequiresRationaleForNonBranchingGraphs(t *testing.T) {
	tool := &TaskGraphTool{Service: &taskGraphServiceStub{}, ParentConversationID: "parent"}
	for _, tasks := range [][]taskGraphTask{
		{{ID: "only", Title: "Only", Prompt: "Work"}},
		{
			{ID: "one", Title: "One", Prompt: "One"},
			{ID: "two", Title: "Two", Prompt: "Two", Dependencies: []string{"one"}},
			{ID: "three", Title: "Three", Prompt: "Three", Dependencies: []string{"two"}},
		},
	} {
		req := taskGraphInput{Action: "create", Title: "Graph", Tasks: tasks}
		if _, err := tool.validateCreate(req); err == nil || !strings.Contains(err.Error(), "serial_rationale") {
			t.Fatalf("validateCreate(%+v) error = %v, want serial_rationale error", tasks, err)
		}
		req.SerialRationale = "The build requires the research artifact produced by the upstream task."
		if _, err := tool.validateCreate(req); err != nil {
			t.Fatalf("validateCreate(%+v) with rationale: %v", tasks, err)
		}
	}
}

func TestTaskGraphToolRejectsInvalidConcurrentScopesAndCycles(t *testing.T) {
	tool := &TaskGraphTool{Service: &taskGraphServiceStub{}, ParentConversationID: "parent"}
	for _, tasks := range [][]taskGraphTask{
		{
			{ID: "one", Title: "One", Prompt: "one", FileScopes: []string{"server"}},
			{ID: "two", Title: "Two", Prompt: "two", FileScopes: []string{"server/api"}},
		},
		{
			{ID: "one", Title: "One", Prompt: "one", Dependencies: []string{"two"}},
			{ID: "two", Title: "Two", Prompt: "two", Dependencies: []string{"one"}},
		},
		{
			{ID: "one", Title: "One"},
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
