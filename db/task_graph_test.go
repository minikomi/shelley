package db

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestTaskGraphLifecycle(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)

	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Ship it", []TaskGraphTaskCreate{
		{ID: "plan", Title: "Plan", Owner: "parent"},
		{ID: "build", Title: "Build", Owner: "subagent", Prompt: "Build it", Dependencies: []string{"plan"}, FileScopes: []string{"server"}},
		{ID: "review", Title: "Review", Owner: "parent", Dependencies: []string{"build"}},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	if got := taskGraphStatus(graph, "plan"); got != "ready" {
		t.Fatalf("plan status = %q, want ready", got)
	}
	if got := taskGraphStatus(graph, "build"); got != "pending" {
		t.Fatalf("build status = %q, want pending", got)
	}
	if graph.Tasks[0].ID != "plan" || graph.Tasks[1].ID != "build" || graph.Tasks[2].ID != "review" {
		t.Fatalf("task order = %q, %q, %q; want input order", graph.Tasks[0].ID, graph.Tasks[1].ID, graph.Tasks[2].ID)
	}

	graph, err = database.CompleteParentTaskGraphTask(t.Context(), graph.GraphID, "plan", "planned")
	if err != nil {
		t.Fatalf("CompleteParentTaskGraphTask: %v", err)
	}
	if got := taskGraphStatus(graph, "build"); got != "ready" {
		t.Fatalf("build status after plan = %q, want ready", got)
	}

	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "build" {
		t.Fatalf("claimed = %+v, want build", claimed)
	}
	child, err := database.CreateSubagentConversation(t.Context(), "child", parentID, nil)
	if err != nil {
		t.Fatalf("CreateSubagentConversation: %v", err)
	}
	if err := database.SetTaskGraphTaskChildConversation(t.Context(), graph.GraphID, "build", child.ConversationID, "child"); err != nil {
		t.Fatalf("SetTaskGraphTaskChildConversation: %v", err)
	}
	childTag := ParseConversationOptions(child.ConversationOptions).TaskGraphChild
	if childTag != nil {
		t.Fatal("stale child options unexpectedly included a graph tag")
	}
	taggedChild, err := database.GetConversationByID(t.Context(), child.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID: %v", err)
	}
	childTag = ParseConversationOptions(taggedChild.ConversationOptions).TaskGraphChild
	if childTag == nil || childTag.ParentConversationID != parentID || childTag.GraphID != graph.GraphID || childTag.TaskID != "build" {
		t.Fatalf("child graph tag = %#v", childTag)
	}
	matched, graph, err := database.CompleteTaskGraphChild(t.Context(), child.ConversationID, "built")
	if err != nil {
		t.Fatalf("CompleteTaskGraphChild: %v", err)
	}
	if !matched {
		t.Fatal("CompleteTaskGraphChild did not match task")
	}
	if got := taskGraphStatus(graph, "review"); got != "ready" {
		t.Fatalf("review status after build = %q, want ready", got)
	}

	matched, _, err = database.CompleteTaskGraphChild(t.Context(), child.ConversationID, "duplicate")
	if err != nil {
		t.Fatalf("duplicate CompleteTaskGraphChild: %v", err)
	}
	if !matched {
		t.Fatal("duplicate completion did not match task")
	}
}

func TestTaskGraphClaimCapsConcurrentSubagents(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)

	tasks := make([]TaskGraphTaskCreate, 4)
	for i := range tasks {
		tasks[i] = TaskGraphTaskCreate{ID: string(rune('a' + i)), Title: "Task", Owner: "subagent", Prompt: "work"}
	}
	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Parallel", tasks)
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d tasks, want 3", len(claimed))
	}
	again, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil {
		t.Fatalf("second ClaimReadyTaskGraphSubagentTasks: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second claim = %d tasks, want 0", len(again))
	}
}

func TestTaskGraphLaunchFailureCancelsDependents(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)

	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Failure", []TaskGraphTaskCreate{
		{ID: "build", Title: "Build", Owner: "subagent", Prompt: "Build"},
		{ID: "review", Title: "Review", Owner: "parent", Dependencies: []string{"build"}},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks = %d, %v; want one task", len(claimed), err)
	}
	graph, err = database.FailTaskGraphTask(t.Context(), graph.GraphID, "build", "model unavailable")
	if err != nil {
		t.Fatalf("FailTaskGraphTask: %v", err)
	}
	if got := taskGraphStatus(graph, "build"); got != "failed" {
		t.Fatalf("build state = %q, want failed", got)
	}
	if got := taskGraphStatus(graph, "review"); got != "cancelled" {
		t.Fatalf("review state = %q, want cancelled", got)
	}
	if graph.Status != "failed" {
		t.Fatalf("graph state = %q, want failed", graph.Status)
	}
}

func TestTaskGraphRecoversClaimWithoutChild(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)
	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Recover", []TaskGraphTaskCreate{
		{ID: "work", Title: "Work", Owner: "subagent", Prompt: "Work"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = %d, %v; want one task", len(claimed), err)
	}
	if err := database.ResetUnstartedTaskGraphTasks(t.Context()); err != nil {
		t.Fatalf("ResetUnstartedTaskGraphTasks: %v", err)
	}
	claimed, err = database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("recovered claim = %d, %v; want one task", len(claimed), err)
	}
}

func TestTaskGraphConcurrentChildCompletionPreservesBothUpdates(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)
	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Parallel", []TaskGraphTaskCreate{
		{ID: "one", Title: "One", Owner: "subagent", Prompt: "One"},
		{ID: "two", Title: "Two", Owner: "subagent", Prompt: "Two"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	claimed, err := database.ClaimReadyTaskGraphSubagentTasks(t.Context(), graph.GraphID)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("ClaimReadyTaskGraphSubagentTasks = %d, %v; want two tasks", len(claimed), err)
	}

	children := make([]string, 0, len(claimed))
	for _, task := range claimed {
		child, err := database.CreateSubagentConversation(t.Context(), task.ID, parentID, nil)
		if err != nil {
			t.Fatalf("CreateSubagentConversation(%q): %v", task.ID, err)
		}
		if err := database.SetTaskGraphTaskChildConversation(t.Context(), graph.GraphID, task.ID, child.ConversationID, task.ID); err != nil {
			t.Fatalf("SetTaskGraphTaskChildConversation(%q): %v", task.ID, err)
		}
		children = append(children, child.ConversationID)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(children))
	for _, childID := range children {
		wg.Add(1)
		go func(childID string) {
			defer wg.Done()
			matched, _, err := database.CompleteTaskGraphChild(t.Context(), childID, childID+" done")
			if err != nil {
				errs <- err
			} else if !matched {
				errs <- errors.New("completion did not match graph child")
			}
		}(childID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	snapshot, err := database.GetTaskGraphSnapshot(t.Context(), graph.GraphID)
	if err != nil {
		t.Fatalf("GetTaskGraphSnapshot: %v", err)
	}
	if snapshot.Status != "complete" || taskGraphStatus(snapshot, "one") != "complete" || taskGraphStatus(snapshot, "two") != "complete" {
		t.Fatalf("snapshot after concurrent completions = %#v", snapshot)
	}
}

func TestTaskGraphDeletedWithParentConversation(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parentID := createTaskGraphParent(t, database)
	graph, err := database.CreateTaskGraph(t.Context(), parentID, "Delete", []TaskGraphTaskCreate{
		{ID: "work", Title: "Work", Owner: "parent"},
	})
	if err != nil {
		t.Fatalf("CreateTaskGraph: %v", err)
	}
	if err := database.DeleteConversation(t.Context(), parentID); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := database.GetTaskGraphSnapshot(t.Context(), graph.GraphID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskGraphSnapshot after delete = %v, want sql.ErrNoRows", err)
	}
}

func createTaskGraphParent(t *testing.T, database *DB) string {
	t.Helper()
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return parent.ConversationID
}

func taskGraphStatus(graph *TaskGraphSnapshot, id string) string {
	for _, task := range graph.Tasks {
		if task.ID == id {
			return task.Status
		}
	}
	return ""
}
