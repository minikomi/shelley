package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"shelley.exe.dev/db"
)

// CreateTaskGraph persists a validated graph before scheduling its ready
// subagent tasks. Launches happen outside the persistence transaction.
func (s *Server) CreateTaskGraph(ctx context.Context, parentID, title string, maxConcurrency int, tasks []db.TaskGraphTaskCreate) (*db.TaskGraphSnapshot, error) {
	if _, err := s.db.GetConversationByID(ctx, parentID); err != nil {
		return nil, err
	}
	latest, err := s.db.GetLatestTaskGraphSnapshot(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Status == "active" {
		return nil, fmt.Errorf("conversation already has an active task graph %q", latest.GraphID)
	}
	snapshot, err := s.db.CreateTaskGraph(ctx, parentID, title, maxConcurrency, tasks)
	if err != nil {
		return nil, err
	}
	s.signalTaskGraphChange()
	go s.scheduleTaskGraph(snapshot.GraphID)
	return snapshot, nil
}

func (s *Server) GetLatestTaskGraphSnapshot(ctx context.Context, parentID string) (*db.TaskGraphSnapshot, error) {
	return s.db.GetLatestTaskGraphSnapshot(ctx, parentID)
}

func (s *Server) GetTaskGraphSnapshot(ctx context.Context, graphID string) (*db.TaskGraphSnapshot, error) {
	return s.db.GetTaskGraphSnapshot(ctx, graphID)
}

func (s *Server) AwaitTaskGraph(ctx context.Context, parentID, graphID string, taskIDs []string) (*db.TaskGraphSnapshot, error) {
	for {
		wake := s.taskGraphWaitChannel()
		snapshot, err := s.db.GetTaskGraphSnapshot(ctx, graphID)
		if err != nil {
			return nil, err
		}
		if snapshot.ParentConversationID != parentID {
			return nil, fmt.Errorf("task graph not found")
		}
		if err := validateTaskGraphAwaitTasks(snapshot, taskIDs); err != nil {
			return nil, err
		}
		if taskGraphAwaited(snapshot, taskIDs) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func validateTaskGraphAwaitTasks(snapshot *db.TaskGraphSnapshot, taskIDs []string) error {
	for _, taskID := range taskIDs {
		found := false
		for _, task := range snapshot.Tasks {
			if task.ID == taskID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("task %q not found", taskID)
		}
	}
	return nil
}

func (s *Server) CancelTaskGraph(ctx context.Context, parentID, graphID string, taskIDs []string) (*db.TaskGraphSnapshot, []string, error) {
	if err := s.ensureTaskGraphParent(ctx, graphID, parentID); err != nil {
		return nil, nil, err
	}
	running, snapshot, err := s.db.CancelTaskGraph(ctx, graphID, taskIDs)
	if err != nil {
		return nil, nil, err
	}
	taskByID := make(map[string]db.TaskGraphTask, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		taskByID[task.ID] = task
	}
	var uncancelled []string
	for _, taskID := range running {
		task := taskByID[taskID]
		if task.ChildConversationID == "" {
			uncancelled = append(uncancelled, taskID)
			continue
		}
		manager, err := s.getOrCreateSubagentConversationManager(ctx, task.ChildConversationID)
		if err != nil {
			uncancelled = append(uncancelled, taskID)
			continue
		}
		if err := manager.CancelConversation(ctx); err != nil {
			uncancelled = append(uncancelled, taskID)
			continue
		}
		if _, err := s.db.CancelRunningTaskGraphTask(ctx, graphID, taskID); err != nil {
			uncancelled = append(uncancelled, taskID)
		}
	}
	snapshot, err = s.db.GetTaskGraphSnapshot(ctx, graphID)
	if err != nil {
		return nil, nil, err
	}
	s.signalTaskGraphChange()
	return snapshot, uncancelled, nil
}

func (s *Server) cancelActiveTaskGraph(ctx context.Context, parentID string) error {
	snapshot, err := s.db.GetLatestTaskGraphSnapshot(ctx, parentID)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.Status != "active" {
		return nil
	}
	_, uncancelled, err := s.CancelTaskGraph(ctx, parentID, snapshot.GraphID, nil)
	if err != nil {
		return err
	}
	if len(uncancelled) != 0 {
		return fmt.Errorf("task graph tasks could not be cancelled: %s", strings.Join(uncancelled, ", "))
	}
	return nil
}

func (s *Server) ensureTaskGraphParent(ctx context.Context, graphID, parentID string) error {
	snapshot, err := s.db.GetTaskGraphSnapshot(ctx, graphID)
	if err != nil {
		return err
	}
	if snapshot.ParentConversationID != parentID {
		return fmt.Errorf("task graph not found")
	}
	return nil
}

func taskGraphAwaited(snapshot *db.TaskGraphSnapshot, taskIDs []string) bool {
	if len(taskIDs) == 0 {
		return snapshot.Status == "complete" || snapshot.Status == "cancelled" || snapshot.Status == "failed"
	}
	wanted := make(map[string]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		wanted[taskID] = true
	}
	for _, task := range snapshot.Tasks {
		if wanted[task.ID] && task.Status != "complete" && task.Status != "cancelled" && task.Status != "failed" {
			return false
		}
		delete(wanted, task.ID)
	}
	return len(wanted) == 0
}

func (s *Server) signalTaskGraphChange() {
	s.taskGraphWakeMu.Lock()
	close(s.taskGraphWake)
	s.taskGraphWake = make(chan struct{})
	s.taskGraphWakeMu.Unlock()
}

func (s *Server) taskGraphWaitChannel() <-chan struct{} {
	s.taskGraphWakeMu.Lock()
	defer s.taskGraphWakeMu.Unlock()
	return s.taskGraphWake
}

func (s *Server) recoverTaskGraphs() {
	if err := s.db.ResetUnstartedTaskGraphTasks(context.Background()); err != nil {
		s.logger.Error("Recover unstarted task graph tasks", "error", err)
		return
	}
	graphIDs, err := s.db.ListTaskGraphsWithReadySubagentTasks(context.Background())
	if err != nil {
		s.logger.Error("Recover task graphs", "error", err)
		return
	}
	for _, graphID := range graphIDs {
		s.scheduleTaskGraph(graphID)
	}
}

func (s *Server) scheduleTaskGraphForChild(childConversationID string) {
	task, err := s.taskGraphTaskForChild(context.Background(), childConversationID)
	if err != nil {
		s.logger.Error("Find task graph for completed child", "conversationID", childConversationID, "error", err)
		return
	}
	if task != nil {
		s.scheduleTaskGraph(task.GraphID)
	}
}

func (s *Server) taskGraphTaskForChild(ctx context.Context, childConversationID string) (*db.TaskGraphChild, error) {
	task, err := s.db.GetTaskGraphChild(ctx, childConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return task, err
}

// scheduleTaskGraph claims a bounded deterministic batch. Every call only
// starts tasks it claimed durably, so duplicate triggers cannot double-start a
// child conversation.
func (s *Server) scheduleTaskGraph(graphID string) {
	tasks, err := s.db.ClaimReadyTaskGraphSubagentTasks(context.Background(), graphID)
	if err != nil {
		s.logger.Error("Claim task graph tasks", "graphID", graphID, "error", err)
		return
	}
	for _, task := range tasks {
		s.launchTaskGraphTask(graphID, task)
	}
	if len(tasks) > 0 {
		s.signalTaskGraphChange()
	}
}

func (s *Server) launchTaskGraphTask(graphID string, task db.TaskGraphTask) {
	ctx := context.Background()
	graph, err := s.db.GetTaskGraphSnapshot(ctx, graphID)
	if err != nil {
		s.logger.Error("Load task graph for launch", "graphID", graphID, "error", err)
		s.failTaskGraphLaunch(graphID, task.ID, err)
		return
	}
	parent, err := s.db.GetConversationByID(ctx, graph.ParentConversationID)
	if err != nil {
		s.logger.Error("Load task graph parent for launch", "graphID", graphID, "error", err)
		s.failTaskGraphLaunch(graphID, task.ID, err)
		return
	}
	cwd := ""
	if parent.Cwd != nil {
		cwd = *parent.Cwd
	}
	slug := task.ID
	if task.Slug != "" {
		slug = task.Slug
	}
	slug = "task-" + strings.ReplaceAll(graphID, "-", "") + "-" + slug
	childID, actualSlug, err := (&db.SubagentDBAdapter{DB: s.db}).GetOrCreateSubagentConversation(ctx, slug, graph.ParentConversationID, cwd)
	if err != nil {
		s.logger.Error("Create task graph child conversation", "graphID", graphID, "taskID", task.ID, "error", err)
		s.failTaskGraphLaunch(graphID, task.ID, err)
		return
	}
	if err := s.db.SetTaskGraphTaskChildConversation(ctx, graphID, task.ID, childID, actualSlug); err != nil {
		s.logger.Error("Associate task graph child conversation", "graphID", graphID, "taskID", task.ID, "error", err)
		s.failTaskGraphLaunch(graphID, task.ID, err)
		return
	}
	model := ""
	if task.Model != "" {
		model = task.Model
	} else if parent.Model != nil {
		model = *parent.Model
	}
	reasoning := ""
	if task.Reasoning != "" {
		reasoning = task.Reasoning
	} else {
		reasoning = db.ParseConversationOptions(parent.ConversationOptions).ThinkingLevel
	}
	prompt := taskGraphTaskPrompt(graph, task)
	if _, err := NewSubagentRunner(s).RunSubagent(ctx, childID, prompt, false, 0, model, reasoning); err != nil {
		s.logger.Error("Launch task graph child", "graphID", graphID, "taskID", task.ID, "error", err)
		s.failTaskGraphLaunch(graphID, task.ID, err)
	}
}

func taskGraphTaskPrompt(graph *db.TaskGraphSnapshot, task db.TaskGraphTask) string {
	var dependencies []db.TaskGraphTask
	for _, dependencyID := range task.Dependencies {
		for _, candidate := range graph.Tasks {
			if candidate.ID == dependencyID && candidate.Status == "complete" {
				dependencies = append(dependencies, candidate)
				break
			}
		}
	}
	if len(dependencies) == 0 {
		return task.Prompt
	}

	var prompt strings.Builder
	prompt.WriteString(task.Prompt)
	prompt.WriteString("\n\nCompleted direct dependency results follow. Use them as handoff context and verify claims against the shared working tree.")
	for _, dependency := range dependencies {
		result := dependency.FinalResponse
		if result == "" {
			result = "(completed without a final response)"
		}
		fmt.Fprintf(&prompt, "\n\n--- dependency %s: %s ---\n%s\n--- end dependency %s ---",
			dependency.ID, dependency.Title, result, dependency.ID)
	}
	return prompt.String()
}

func (s *Server) failTaskGraphLaunch(graphID, taskID string, cause error) {
	if _, err := s.db.FailTaskGraphTask(context.Background(), graphID, taskID, cause.Error()); err != nil {
		s.logger.Error("Mark task graph task failed", "graphID", graphID, "taskID", taskID, "error", err)
	}
	s.signalTaskGraphChange()
}

func (s *Server) handleGetTaskGraph(w http.ResponseWriter, r *http.Request, parentID string) {
	snapshot, err := s.db.GetLatestTaskGraphSnapshot(r.Context(), parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "task graph not found", http.StatusNotFound)
			return
		}
		s.logger.Error("Get task graph", "conversationID", parentID, "error", err)
		http.Error(w, "failed to get task graph", http.StatusInternalServerError)
		return
	}
	if snapshot == nil {
		http.Error(w, "task graph not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		s.logger.Error("Encode task graph", "conversationID", parentID, "error", err)
	}
}
