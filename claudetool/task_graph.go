package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

const taskGraphName = "task_graph"

const taskGraphDescription = `Create and coordinate a persisted task dependency graph.

For a top-level request with multiple substantial stages, or with independent
research, implementation, or review scopes, you MUST call create before
substantial research, implementation, or browser work. Before the first graph,
one bounded read-only shell pass may inspect repository state, guidance files,
manifests, and available test commands so the graph reflects real work rather
than guessed audit tasks. A prose plan is not a task graph. Do not create a
graph for contained work where delegation would not shorten the critical path.

When the user already supplies a concrete contract, acceptance criteria, and
identifiable implementation scopes, do only cheap bounded inspection needed to
stabilize interfaces, then delegate implementation directly. Keep that graph
minimal: do not fan out discovery or research subagents. Discovery or research
tasks are justified only when independent unknowns genuinely block
implementation and the parent cannot resolve them cheaply.
Each implementation owner includes focused tests for its scope.
Do not create separate test-mapping or review filler tasks.
The parent owns integration and final validation.

Choose task boundaries by cohesive, independently testable outcomes, not by
file type. A task is right-sized when it is substantial enough to amortize
delegation overhead, has one clear responsibility, and can finish without
frequent coordination with concurrent tasks. Keep tightly coupled work such as
markup, styling, and browser behavior under one owner when they share a runtime
contract.

Each task prompt is a bounded context packet: state the objective, acceptance
criteria, owned scope, stable interfaces, source-of-truth files, non-goals, and
focused validation command. Do not mechanically inherit the parent's model or
reasoning level; use the least costly capable model and medium reasoning for
routine bounded work unless genuine complexity justifies more.

Use create once per delegation wave to define delegated subagent runs only.
Keep parent planning, integration, and final validation outside the graph.
Every task launches a subagent automatically when its dependencies complete.
Before a dependent task launches, Shelley appends every completed direct
dependency's final response to its prompt as handoff context. Do not duplicate
those results in task prompts; shared files remain the source of truth.
If a task must read, use, or coordinate with output owned by another task, add
that dependency unless the parent established an immutable interface before
the wave. Never consume another task's partially written files.
Every task's final response reports its deliverable, validation performed,
remaining uncertainty, and exact parent integration needs, concisely.
Use await to wait for work already in progress without sending children any
new prompts. Every result includes the complete current graph snapshot.

After a graph reaches a terminal state, reassess the remaining request. If the
next work has independent scopes where delegation shortens the critical path,
call create again for a new graph before starting that work. Each graph should
represent one coherent wave based on what is known then; do not hardcode phase
names or task counts, and do not mutate a finished graph: completed waves are immutable.

After a graph becomes terminal, integrate through the cheap path first: read
task reports and git diff --stat, run one aggregate acceptance command, and
inspect full files only when a check fails or an interface violation is
suspected. Do not repeat focused checks that task owners already passed. Do not
silently rewrite a child's substantial scope in the parent; use a focused
follow-up task or reconsider the partition when integration requires major
rework.

Subagents are not research-only advisors. A delegation wave may own
implementation and modify files when scopes are exclusive and clearly assigned.
Do not begin substantial parent-side implementation merely because an earlier
research wave finished. First decide whether the build work can be partitioned;
if it can, create another graph and delegate those file-writing scopes. The
parent integrates results and handles work that cannot usefully be delegated.

Before launching file-writing tasks, establish any shared manifests, module
contracts, directories, and test infrastructure those tasks need. While a graph
is active, the parent may work on non-overlapping integration files, but must
not change shared configuration, interfaces, or test/runtime infrastructure
consumed by running children. Await or cancel the affected tasks first.

Choose the graph size and shape from the actual work. max_concurrency controls
simultaneously running children, not graph size; omit it to use the server
default of 3, or set another positive value when resource and cost constraints
warrant it. Branch whenever two useful subagent scopes can start from what is
already known. A singleton or fully serial graph is an exception and requires
serial_rationale explaining the concrete upstream output that blocks every
other delegated scope. Parent-owned work is not a reason to omit a later
implementation or review wave. Dependencies must represent true blockers, not
preferred ordering. Do not invent audit, research, design, review, or
implementation tasks merely to fill slots, create symmetry, or make the graph
look busy.`

type TaskGraphService interface {
	CreateTaskGraph(context.Context, string, string, int, []db.TaskGraphTaskCreate) (*db.TaskGraphSnapshot, error)
	GetLatestTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error)
	GetTaskGraphSnapshot(context.Context, string) (*db.TaskGraphSnapshot, error)
	AwaitTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, error)
	CancelTaskGraph(context.Context, string, string, []string) (*db.TaskGraphSnapshot, []string, error)
}

type TaskGraphTool struct {
	Service              TaskGraphService
	ParentConversationID string
	AvailableModels      []AvailableModel
}

type taskGraphInput struct {
	Action          string          `json:"action"`
	GraphID         string          `json:"graph_id,omitempty"`
	Title           string          `json:"title,omitempty"`
	Tasks           []taskGraphTask `json:"tasks,omitempty"`
	TaskIDs         []string        `json:"task_ids,omitempty"`
	Timeout         int             `json:"timeout_seconds,omitempty"`
	MaxConcurrency  int             `json:"max_concurrency,omitempty"`
	SerialRationale string          `json:"serial_rationale,omitempty"`
}

type taskGraphTask struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Dependencies []string `json:"dependencies,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	Slug         string   `json:"slug,omitempty"`
	Model        string   `json:"model,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	FileScopes   []string `json:"file_scopes,omitempty"`
}

func (t *TaskGraphTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        taskGraphName,
		Description: taskGraphDescription,
		InputSchema: llm.MustSchema(`{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {"type": "string", "enum": ["create", "list", "await", "cancel"]},
    "graph_id": {"type": "string", "description": "Graph ID. list may omit it to get the latest graph."},
    "title": {"type": "string", "description": "Required for create."},
    "max_concurrency": {"type": "integer", "minimum": 1, "description": "Maximum simultaneously running children for this graph. Omit to use the server default of 3."},
    "serial_rationale": {"type": "string", "description": "Required for a singleton or fully serial graph. Explain the concrete upstream output that prevents another delegated scope from starting now."},
    "tasks": {
      "type": "array",
      "description": "Required for create.",
      "items": {
        "type": "object",
        "required": ["id", "title", "prompt"],
        "properties": {
          "id": {"type": "string"},
          "title": {"type": "string"},
          "dependencies": {"type": "array", "items": {"type": "string"}},
          "prompt": {"type": "string", "description": "The complete delegated scope for this subagent run."},
          "slug": {"type": "string"},
          "model": {"type": "string"},
          "reasoning": {"type": "string", "enum": ["off", "minimal", "low", "medium", "high", "xhigh", "max"]},
          "file_scopes": {"type": "array", "items": {"type": "string"}}
        }
      }
    },
    "task_ids": {"type": "array", "items": {"type": "string"}, "description": "Tasks to await or cancel. Omit to await/cancel the whole graph."},
    "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 3600, "description": "Maximum await duration. Defaults to 900 seconds."}
  }
}`),
		Run: llm.RunJSON(t.run),
	}
}

func (t *TaskGraphTool) run(ctx context.Context, req taskGraphInput) llm.ToolOut {
	if t.Service == nil {
		return llm.ErrorfToolOut("task graph service is unavailable")
	}
	switch req.Action {
	case "create":
		tasks, err := t.validateCreate(req)
		if err != nil {
			return llm.ErrorfToolOut("%v", err)
		}
		snapshot, err := t.Service.CreateTaskGraph(ctx, t.ParentConversationID, strings.TrimSpace(req.Title), req.MaxConcurrency, tasks)
		if err != nil {
			return llm.ErrorfToolOut("create task graph: %v", err)
		}
		return taskGraphToolOut("Task graph created.", snapshot)
	case "list":
		snapshot, err := t.snapshot(ctx, req.GraphID)
		if err != nil {
			return llm.ErrorfToolOut("list task graph: %v", err)
		}
		if snapshot == nil {
			return llm.ToolOut{LLMContent: llm.TextContent("No task graph exists for this conversation.")}
		}
		return taskGraphToolOut("Current task graph.", snapshot)
	case "await":
		if req.GraphID == "" {
			return llm.ErrorfToolOut("graph_id is required for await")
		}
		timeout := req.Timeout
		if timeout == 0 {
			timeout = 900
		}
		if timeout < 1 || timeout > 3600 {
			return llm.ErrorfToolOut("timeout_seconds must be between 1 and 3600")
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		snapshot, err := t.Service.AwaitTaskGraph(waitCtx, t.ParentConversationID, req.GraphID, req.TaskIDs)
		if err != nil {
			return llm.ErrorfToolOut("await task graph: %v", err)
		}
		message := "Await condition reached."
		if snapshot != nil && snapshot.Status != "active" {
			message += " This delegation wave is terminal. Integrate through the cheap path first: read task reports and git diff --stat, run one aggregate acceptance command, and inspect full files only for failures or suspected interface violations. Do not repeat passing focused checks or substantially rewrite child-owned work in the parent. Before using research, shell, editing, or browser tools for remaining substantial work, decide whether another delegation wave would shorten the critical path. If so, create a new graph first; implementation tasks may own exclusive file scopes."
		}
		return taskGraphToolOut(message, snapshot)
	case "cancel":
		if req.GraphID == "" {
			return llm.ErrorfToolOut("graph_id is required for cancel")
		}
		snapshot, running, err := t.Service.CancelTaskGraph(ctx, t.ParentConversationID, req.GraphID, req.TaskIDs)
		if err != nil {
			return llm.ErrorfToolOut("cancel task graph: %v", err)
		}
		if len(running) > 0 {
			return taskGraphToolOut(
				fmt.Sprintf("Queued and ready tasks were cancelled. Running tasks could not be cancelled safely: %s.", strings.Join(running, ", ")),
				snapshot,
			)
		}
		return taskGraphToolOut("Tasks cancelled.", snapshot)
	default:
		return llm.ErrorfToolOut("unknown action %q", req.Action)
	}
}

func (t *TaskGraphTool) snapshot(ctx context.Context, graphID string) (*db.TaskGraphSnapshot, error) {
	if graphID == "" {
		return t.Service.GetLatestTaskGraphSnapshot(ctx, t.ParentConversationID)
	}
	snapshot, err := t.Service.GetTaskGraphSnapshot(ctx, graphID)
	if err != nil || snapshot == nil {
		return snapshot, err
	}
	if snapshot.ParentConversationID != t.ParentConversationID {
		return nil, fmt.Errorf("task graph not found")
	}
	return snapshot, nil
}

func (t *TaskGraphTool) validateCreate(req taskGraphInput) ([]db.TaskGraphTaskCreate, error) {
	if strings.TrimSpace(req.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if req.MaxConcurrency < 0 {
		return nil, fmt.Errorf("max_concurrency must be positive")
	}
	if len(req.Tasks) == 0 {
		return nil, fmt.Errorf("at least one delegated task is required")
	}
	byID := make(map[string]taskGraphTask, len(req.Tasks))
	orderedIDs := make([]string, 0, len(req.Tasks))
	for _, task := range req.Tasks {
		task.ID = strings.TrimSpace(task.ID)
		task.Title = strings.TrimSpace(task.Title)
		if task.ID == "" || task.Title == "" {
			return nil, fmt.Errorf("every task requires id and title")
		}
		if _, exists := byID[task.ID]; exists {
			return nil, fmt.Errorf("duplicate task id %q", task.ID)
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return nil, fmt.Errorf("task %q requires a subagent prompt", task.ID)
		}
		if task.Reasoning != "" && !isValidReasoningLevel(task.Reasoning) {
			return nil, fmt.Errorf("task %q has invalid reasoning %q", task.ID, task.Reasoning)
		}
		if task.Model != "" && !t.hasModel(task.Model) {
			return nil, fmt.Errorf("task %q has unknown model %q", task.ID, task.Model)
		}
		if task.Slug != "" {
			task.Slug = sanitizeSlug(task.Slug)
			if task.Slug == "" {
				return nil, fmt.Errorf("task %q slug must contain alphanumeric characters", task.ID)
			}
		}
		for i, scope := range task.FileScopes {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				return nil, fmt.Errorf("task %q has an empty file scope", task.ID)
			}
			task.FileScopes[i] = scope
		}
		byID[task.ID] = task
		orderedIDs = append(orderedIDs, task.ID)
	}
	for _, task := range byID {
		seen := make(map[string]bool, len(task.Dependencies))
		for _, dep := range task.Dependencies {
			if dep == task.ID {
				return nil, fmt.Errorf("task %q cannot depend on itself", task.ID)
			}
			if _, exists := byID[dep]; !exists {
				return nil, fmt.Errorf("task %q depends on unknown task %q", task.ID, dep)
			}
			if seen[dep] {
				return nil, fmt.Errorf("task %q repeats dependency %q", task.ID, dep)
			}
			seen[dep] = true
		}
	}
	if err := validateTaskGraphAcyclic(byID); err != nil {
		return nil, err
	}
	hasParallelPair := false
	for i, leftID := range orderedIDs {
		for _, rightID := range orderedIDs[i+1:] {
			left, right := byID[leftID], byID[rightID]
			if taskDependsOn(byID, left.ID, right.ID) || taskDependsOn(byID, right.ID, left.ID) {
				continue
			}
			hasParallelPair = true
			if fileScopesOverlap(left.FileScopes, right.FileScopes) {
				return nil, fmt.Errorf("concurrent tasks %q and %q have overlapping file scopes", left.ID, right.ID)
			}
		}
	}
	if !hasParallelPair && strings.TrimSpace(req.SerialRationale) == "" {
		return nil, fmt.Errorf("serial_rationale is required when no delegated tasks can run in parallel")
	}
	out := make([]db.TaskGraphTaskCreate, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		task := byID[id]
		out = append(out, db.TaskGraphTaskCreate{
			ID: task.ID, Title: task.Title, Dependencies: task.Dependencies,
			Prompt: task.Prompt, Slug: task.Slug, Model: task.Model, Reasoning: task.Reasoning, FileScopes: task.FileScopes,
		})
	}
	return out, nil
}

func (t *TaskGraphTool) hasModel(model string) bool {
	if len(t.AvailableModels) == 0 {
		return true
	}
	return slices.ContainsFunc(t.AvailableModels, func(available AvailableModel) bool { return available.ID == model })
}

func validateTaskGraphAcyclic(tasks map[string]taskGraphTask) error {
	visiting := make(map[string]bool, len(tasks))
	visited := make(map[string]bool, len(tasks))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("task graph has a dependency cycle involving %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range tasks[id].Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range tasks {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func taskDependsOn(tasks map[string]taskGraphTask, taskID, targetID string) bool {
	for _, dependency := range tasks[taskID].Dependencies {
		if dependency == targetID || taskDependsOn(tasks, dependency, targetID) {
			return true
		}
	}
	return false
}

func fileScopesOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if fileScopeOverlaps(a, b) {
				return true
			}
		}
	}
	return false
}

func fileScopeOverlaps(a, b string) bool {
	a = strings.TrimSuffix(path.Clean(strings.TrimSuffix(a, "**")), "/")
	b = strings.TrimSuffix(path.Clean(strings.TrimSuffix(b, "**")), "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func taskGraphToolOut(text string, snapshot *db.TaskGraphSnapshot) llm.ToolOut {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return llm.ErrorfToolOut("encode task graph result: %v", err)
	}
	return llm.ToolOut{
		LLMContent: llm.TextContent(text + "\n" + string(payload)),
		Display:    snapshot,
	}
}
