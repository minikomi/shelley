package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/db/generated"
)

type TaskGraphTaskCreate struct {
	ID           string
	Title        string
	Owner        string
	Dependencies []string
	Prompt       string
	Slug         string
	Model        string
	Reasoning    string
	FileScopes   []string
}

type TaskGraphSnapshot struct {
	GraphID              string          `json:"id"`
	ParentConversationID string          `json:"parent_conversation_id"`
	Title                string          `json:"title"`
	Status               string          `json:"state"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Tasks                []TaskGraphTask `json:"tasks"`
}

type TaskGraphTask struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Owner               string     `json:"owner"`
	Status              string     `json:"state"`
	Dependencies        []string   `json:"depends_on"`
	Prompt              string     `json:"prompt,omitempty"`
	Slug                string     `json:"slug,omitempty"`
	Model               string     `json:"model,omitempty"`
	Reasoning           string     `json:"reasoning,omitempty"`
	FileScopes          []string   `json:"file_scopes,omitempty"`
	ChildConversationID string     `json:"child_conversation_id,omitempty"`
	FinalResponse       string     `json:"result,omitempty"`
	Error               string     `json:"error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
}

func (db *DB) CreateTaskGraph(ctx context.Context, parentConversationID, title string, tasks []TaskGraphTaskCreate) (*TaskGraphSnapshot, error) {
	graphID := uuid.NewString()
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		if _, err := q.CreateTaskGraph(ctx, generated.CreateTaskGraphParams{
			GraphID:              graphID,
			ParentConversationID: parentConversationID,
			Title:                title,
		}); err != nil {
			return err
		}
		for position, task := range tasks {
			fileScopes, err := json.Marshal(task.FileScopes)
			if err != nil {
				return fmt.Errorf("marshal file scopes for %q: %w", task.ID, err)
			}
			if _, err := q.CreateTaskGraphTask(ctx, generated.CreateTaskGraphTaskParams{
				GraphID:    graphID,
				TaskID:     task.ID,
				Position:   int64(position),
				Title:      task.Title,
				Owner:      task.Owner,
				Prompt:     nullString(task.Prompt),
				Slug:       nullString(task.Slug),
				Model:      nullString(task.Model),
				Reasoning:  nullString(task.Reasoning),
				FileScopes: string(fileScopes),
			}); err != nil {
				return err
			}
			for _, dependency := range task.Dependencies {
				if err := q.CreateTaskGraphDependency(ctx, generated.CreateTaskGraphDependencyParams{
					GraphID:         graphID,
					TaskID:          task.ID,
					DependsOnTaskID: dependency,
				}); err != nil {
					return err
				}
			}
		}
		_, err := q.PromoteTaskGraphReadyTasks(ctx, graphID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create task graph: %w", err)
	}
	return db.GetTaskGraphSnapshot(ctx, graphID)
}

func (db *DB) GetLatestTaskGraphSnapshot(ctx context.Context, parentConversationID string) (*TaskGraphSnapshot, error) {
	var graph generated.TaskGraph
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		graph, err = generated.New(rx.Conn()).GetLatestTaskGraph(ctx, parentConversationID)
		return err
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return db.getTaskGraphSnapshot(ctx, graph)
}

func (db *DB) GetTaskGraphSnapshot(ctx context.Context, graphID string) (*TaskGraphSnapshot, error) {
	var graph generated.TaskGraph
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		graph, err = generated.New(rx.Conn()).GetTaskGraph(ctx, graphID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db.getTaskGraphSnapshot(ctx, graph)
}

func (db *DB) ListTaskGraphsWithReadySubagentTasks(ctx context.Context) ([]string, error) {
	var graphIDs []string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		graphIDs, err = generated.New(rx.Conn()).ListTaskGraphsWithReadySubagentTasks(ctx)
		return err
	})
	return graphIDs, err
}

func (db *DB) getTaskGraphSnapshot(ctx context.Context, graph generated.TaskGraph) (*TaskGraphSnapshot, error) {
	var tasks []generated.TaskGraphTask
	var dependencies []generated.TaskGraphDependency
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		tasks, err = q.ListTaskGraphTasks(ctx, graph.GraphID)
		if err != nil {
			return err
		}
		dependencies, err = q.ListTaskGraphDependencies(ctx, graph.GraphID)
		return err
	})
	if err != nil {
		return nil, err
	}
	depsByTask := make(map[string][]string, len(tasks))
	for _, dep := range dependencies {
		depsByTask[dep.TaskID] = append(depsByTask[dep.TaskID], dep.DependsOnTaskID)
	}
	snapshot := &TaskGraphSnapshot{
		GraphID:              graph.GraphID,
		ParentConversationID: graph.ParentConversationID,
		Title:                graph.Title,
		CreatedAt:            graph.CreatedAt,
		UpdatedAt:            graph.UpdatedAt,
		Tasks:                make([]TaskGraphTask, 0, len(tasks)),
	}
	cancelled := false
	failed := false
	terminal := true
	for _, task := range tasks {
		var fileScopes []string
		if err := json.Unmarshal([]byte(task.FileScopes), &fileScopes); err != nil {
			return nil, fmt.Errorf("decode file scopes for %q: %w", task.TaskID, err)
		}
		dependencies := depsByTask[task.TaskID]
		if dependencies == nil {
			dependencies = []string{}
		}
		snapshot.Tasks = append(snapshot.Tasks, TaskGraphTask{
			ID:                  task.TaskID,
			Title:               task.Title,
			Owner:               task.Owner,
			Status:              taskGraphTaskState(task.Status),
			Dependencies:        dependencies,
			Prompt:              taskGraphDeref(task.Prompt),
			Slug:                taskGraphDeref(task.Slug),
			Model:               taskGraphDeref(task.Model),
			Reasoning:           taskGraphDeref(task.Reasoning),
			FileScopes:          fileScopes,
			ChildConversationID: taskGraphDeref(task.ChildConversationID),
			FinalResponse:       taskGraphDeref(task.FinalResponse),
			Error:               taskGraphTaskError(task.Status, task.FinalResponse),
			CreatedAt:           task.CreatedAt,
			StartedAt:           task.StartedAt,
			CompletedAt:         task.CompletedAt,
		})
		if task.Status == "cancelled" {
			cancelled = true
		}
		if task.Status == "failed" {
			failed = true
		}
		if task.Status != "complete" && task.Status != "cancelled" && task.Status != "failed" {
			terminal = false
		}
	}
	switch {
	case terminal && failed:
		snapshot.Status = "failed"
	case terminal && cancelled:
		snapshot.Status = "cancelled"
	case terminal:
		snapshot.Status = "complete"
	default:
		snapshot.Status = "active"
	}
	return snapshot, nil
}

func (db *DB) CompleteParentTaskGraphTask(ctx context.Context, graphID, taskID, response string) (*TaskGraphSnapshot, error) {
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		task, err := q.GetTaskGraphTask(ctx, generated.GetTaskGraphTaskParams{GraphID: graphID, TaskID: taskID})
		if err != nil {
			return err
		}
		if task.Owner != "parent" {
			return fmt.Errorf("task %q is owned by %s", taskID, task.Owner)
		}
		if task.Status == "complete" {
			return nil
		}
		n, err := q.CompleteParentTaskGraphTask(ctx, generated.CompleteParentTaskGraphTaskParams{
			FinalResponse: nullString(response),
			GraphID:       graphID,
			TaskID:        taskID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("task %q is not ready", taskID)
		}
		_, err = q.PromoteTaskGraphReadyTasks(ctx, graphID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db.GetTaskGraphSnapshot(ctx, graphID)
}

// CompleteTaskGraphChild records a final subagent response. It returns true
// only when the conversation belongs to a running graph task.
func (db *DB) CompleteTaskGraphChild(ctx context.Context, childConversationID, response string) (bool, *TaskGraphSnapshot, error) {
	var graphID string
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		task, err := q.FindTaskGraphTaskByChildConversation(ctx, &childConversationID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		graphID = task.GraphID
		n, err := q.CompleteChildTaskGraphTask(ctx, generated.CompleteChildTaskGraphTaskParams{
			FinalResponse:       nullString(response),
			ChildConversationID: &childConversationID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		_, err = q.PromoteTaskGraphReadyTasks(ctx, graphID)
		return err
	})
	if err != nil {
		return false, nil, err
	}
	if graphID == "" {
		return false, nil, nil
	}
	snapshot, err := db.GetTaskGraphSnapshot(ctx, graphID)
	return true, snapshot, err
}

func (db *DB) FailTaskGraphTask(ctx context.Context, graphID, taskID, message string) (*TaskGraphSnapshot, error) {
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		n, err := q.FailTaskGraphTask(ctx, generated.FailTaskGraphTaskParams{
			FinalResponse: nullString(message),
			GraphID:       graphID,
			TaskID:        taskID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		for {
			n, err := q.CancelBlockedTaskGraphTasks(ctx, graphID)
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return db.GetTaskGraphSnapshot(ctx, graphID)
}

func (db *DB) ClaimReadyTaskGraphSubagentTasks(ctx context.Context, graphID string) ([]generated.TaskGraphTask, error) {
	var tasks []generated.TaskGraphTask
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		var err error
		tasks, err = q.ClaimReadyTaskGraphSubagentTasks(ctx, graphID)
		return err
	})
	return tasks, err
}

func (db *DB) SetTaskGraphTaskChildConversation(ctx context.Context, graphID, taskID, conversationID, slug string) error {
	return db.WithTx(ctx, func(q *generated.Queries) error {
		n, err := q.SetTaskGraphTaskChildConversation(ctx, generated.SetTaskGraphTaskChildConversationParams{
			ChildConversationID: &conversationID,
			Slug:                &slug,
			GraphID:             graphID,
			TaskID:              taskID,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("task %q was not available for launch", taskID)
		}
		return nil
	})
}

func (db *DB) ResetUnstartedTaskGraphTasks(ctx context.Context) error {
	return db.WithTx(ctx, func(q *generated.Queries) error {
		_, err := q.ResetUnstartedTaskGraphTasks(ctx)
		return err
	})
}

// CancelTaskGraph cancels queued or ready tasks. The server handles running
// child conversation cancellation separately.
func (db *DB) CancelTaskGraph(ctx context.Context, graphID string, taskIDs []string) ([]string, *TaskGraphSnapshot, error) {
	var running []string
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		if len(taskIDs) == 0 {
			tasks, err := q.ListTaskGraphTasks(ctx, graphID)
			if err != nil {
				return err
			}
			for _, task := range tasks {
				if task.Status == "running" {
					running = append(running, task.TaskID)
				}
			}
			_, err = q.CancelReadyTaskGraphTasks(ctx, graphID)
			if err != nil {
				return err
			}
		} else {
			for _, taskID := range taskIDs {
				task, err := q.GetTaskGraphTask(ctx, generated.GetTaskGraphTaskParams{GraphID: graphID, TaskID: taskID})
				if err != nil {
					return err
				}
				if task.Status == "running" {
					running = append(running, taskID)
					continue
				}
				if _, err := q.CancelReadyTaskGraphTask(ctx, generated.CancelReadyTaskGraphTaskParams{GraphID: graphID, TaskID: taskID}); err != nil {
					return err
				}
			}
		}
		for {
			n, err := q.CancelBlockedTaskGraphTasks(ctx, graphID)
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
		}
	})
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := db.GetTaskGraphSnapshot(ctx, graphID)
	return running, snapshot, err
}

func (db *DB) CancelRunningTaskGraphTask(ctx context.Context, graphID, taskID string) (*TaskGraphSnapshot, error) {
	err := db.WithTx(ctx, func(q *generated.Queries) error {
		n, err := q.CancelRunningTaskGraphTask(ctx, generated.CancelRunningTaskGraphTaskParams{
			GraphID: graphID,
			TaskID:  taskID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		for {
			n, err := q.CancelBlockedTaskGraphTasks(ctx, graphID)
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return db.GetTaskGraphSnapshot(ctx, graphID)
}

func nullString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func taskGraphDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func taskGraphTaskState(status string) string {
	if status == "queued" {
		return "pending"
	}
	return status
}

func taskGraphTaskError(status string, response *string) string {
	if status != "failed" {
		return ""
	}
	return taskGraphDeref(response)
}
