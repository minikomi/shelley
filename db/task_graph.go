package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/db/generated"
)

type TaskGraphTaskCreate struct {
	ID           string
	Title        string
	Dependencies []string
	Prompt       string
	Slug         string
	Model        string
	Reasoning    string
	FileScopes   []string
}

const DefaultTaskGraphMaxConcurrency = 3

// TaskGraphSnapshot is persisted in its parent conversation's options.
type TaskGraphSnapshot struct {
	GraphID              string          `json:"id"`
	ParentConversationID string          `json:"parent_conversation_id"`
	Title                string          `json:"title"`
	Status               string          `json:"state"`
	MaxConcurrency       int             `json:"max_concurrency"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Tasks                []TaskGraphTask `json:"tasks"`
}

type TaskGraphTask struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Owner               string   `json:"owner"`
	Status              string   `json:"state"`
	Dependencies        []string `json:"depends_on"`
	Prompt              string   `json:"prompt,omitempty"`
	Slug                string   `json:"slug,omitempty"`
	Model               string   `json:"model,omitempty"`
	Reasoning           string   `json:"reasoning,omitempty"`
	FileScopes          []string `json:"file_scopes,omitempty"`
	ChildConversationID string   `json:"child_conversation_id,omitempty"`
	// Result is read from the child conversation when a snapshot is served; it is never stored.
	Result      string     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TaskGraphChild tags a graph-owned child conversation, allowing completion
// and restart recovery to find its parent graph without a separate table.
type TaskGraphChild struct {
	ParentConversationID string `json:"parent_conversation_id"`
	GraphID              string `json:"graph_id"`
	TaskID               string `json:"task_id"`
}

type taskGraphOptionsRow struct {
	conversationID string
	opts           ConversationOptions
}

func (db *DB) CreateTaskGraph(ctx context.Context, parentConversationID, title string, maxConcurrency int, tasks []TaskGraphTaskCreate) (*TaskGraphSnapshot, error) {
	if maxConcurrency < 0 {
		return nil, fmt.Errorf("max concurrency must be positive")
	}
	graphID := uuid.NewString()
	var created TaskGraphSnapshot
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		parent, err := q.GetConversation(ctx, parentConversationID)
		if err != nil {
			return err
		}
		opts := ParseConversationOptions(parent.ConversationOptions)
		now := tx.Now
		graph := TaskGraphSnapshot{
			GraphID:              graphID,
			ParentConversationID: parentConversationID,
			Title:                title,
			MaxConcurrency:       taskGraphMaxConcurrency(maxConcurrency),
			CreatedAt:            now,
			UpdatedAt:            now,
			Tasks:                make([]TaskGraphTask, 0, len(tasks)),
		}
		for _, task := range tasks {
			if strings.TrimSpace(task.Prompt) == "" {
				return fmt.Errorf("task %q requires a subagent prompt", task.ID)
			}
			graph.Tasks = append(graph.Tasks, TaskGraphTask{
				ID:           task.ID,
				Title:        task.Title,
				Owner:        "subagent",
				Status:       "pending",
				Dependencies: append([]string{}, task.Dependencies...),
				Prompt:       task.Prompt,
				Slug:         task.Slug,
				Model:        task.Model,
				Reasoning:    task.Reasoning,
				FileScopes:   append([]string{}, task.FileScopes...),
				CreatedAt:    now,
			})
		}
		promoteTaskGraphReadyTasks(&graph)
		refreshTaskGraphStatus(&graph)
		opts.TaskGraphs = append(opts.TaskGraphs, graph)
		if err := saveTaskGraphOptions(ctx, q, parentConversationID, opts); err != nil {
			return err
		}
		created = graph
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create task graph: %w", err)
	}
	return &created, nil
}

func (db *DB) GetLatestTaskGraphSnapshot(ctx context.Context, parentConversationID string) (*TaskGraphSnapshot, error) {
	var graph *TaskGraphSnapshot
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		parent, err := generated.New(rx.Conn()).GetConversation(ctx, parentConversationID)
		if err != nil {
			return err
		}
		opts := ParseConversationOptions(parent.ConversationOptions)
		if len(opts.TaskGraphs) == 0 {
			return nil
		}
		latest := opts.TaskGraphs[len(opts.TaskGraphs)-1]
		refreshTaskGraphStatus(&latest)
		graph = &latest
		return nil
	})
	return graph, err
}

func (db *DB) ListTaskGraphSnapshots(ctx context.Context, parentConversationID string) ([]TaskGraphSnapshot, error) {
	graphs := make([]TaskGraphSnapshot, 0)
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		parent, err := generated.New(rx.Conn()).GetConversation(ctx, parentConversationID)
		if err != nil {
			return err
		}
		opts := ParseConversationOptions(parent.ConversationOptions)
		for _, graph := range opts.TaskGraphs {
			refreshTaskGraphStatus(&graph)
			graphs = append(graphs, graph)
		}
		return nil
	})
	return graphs, err
}

func (db *DB) GetTaskGraphSnapshot(ctx context.Context, parentConversationID, graphID string) (*TaskGraphSnapshot, error) {
	var snapshot TaskGraphSnapshot
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		_, graph, err := findTaskGraphOptions(ctx, generated.New(rx.Conn()), parentConversationID, graphID)
		if err != nil {
			return err
		}
		refreshTaskGraphStatus(graph)
		snapshot = *graph
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (db *DB) ListTaskGraphsWithReadySubagentTasks(ctx context.Context) ([]TaskGraphSnapshot, error) {
	var graphs []TaskGraphSnapshot
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := listTaskGraphOptions(ctx, rx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			for _, graph := range row.opts.TaskGraphs {
				for _, task := range graph.Tasks {
					if task.Owner == "subagent" && task.Status == "ready" {
						graphs = append(graphs, graph)
						break
					}
				}
			}
		}
		return nil
	})
	return graphs, err
}

// CompleteTaskGraphChild marks a finished child task complete. It returns true
// whenever the child is durably tagged as a graph child, including duplicate
// completion notifications.
func (db *DB) CompleteTaskGraphChild(ctx context.Context, childConversationID string) (bool, *TaskGraphSnapshot, error) {
	var matched bool
	var snapshot *TaskGraphSnapshot
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		child, err := q.GetConversation(ctx, childConversationID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		childTag := ParseConversationOptions(child.ConversationOptions).TaskGraphChild
		if childTag == nil {
			return nil
		}
		parent, err := q.GetConversation(ctx, childTag.ParentConversationID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		opts := ParseConversationOptions(parent.ConversationOptions)
		graph := taskGraphByID(&opts, childTag.GraphID)
		if graph == nil {
			return nil
		}
		task := findTaskGraphTask(graph, childTag.TaskID)
		if task == nil || task.ChildConversationID != childConversationID {
			return nil
		}
		matched = true
		if task.Status == "running" {
			task.Status = "complete"
			now := tx.Now
			task.CompletedAt = &now
			graph.UpdatedAt = now
			promoteTaskGraphReadyTasks(graph)
			refreshTaskGraphStatus(graph)
			if err := saveTaskGraphOptions(ctx, q, parent.ConversationID, opts); err != nil {
				return err
			}
		}
		refreshTaskGraphStatus(graph)
		copy := *graph
		snapshot = &copy
		return nil
	})
	return matched, snapshot, err
}

func (db *DB) FailTaskGraphTask(ctx context.Context, parentConversationID, graphID, taskID, message string) (*TaskGraphSnapshot, error) {
	var snapshot TaskGraphSnapshot
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		row, graph, err := findTaskGraphOptions(ctx, q, parentConversationID, graphID)
		if err != nil {
			return err
		}
		task := findTaskGraphTask(graph, taskID)
		if task == nil {
			return sql.ErrNoRows
		}
		if task.Status == "running" {
			task.Status = "failed"
			task.Error = message
			now := tx.Now
			task.CompletedAt = &now
			graph.UpdatedAt = now
			cancelBlockedTaskGraphTasks(graph, now)
		}
		refreshTaskGraphStatus(graph)
		if err := saveTaskGraphOptions(ctx, q, row.conversationID, row.opts); err != nil {
			return err
		}
		snapshot = *graph
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (db *DB) ClaimReadyTaskGraphSubagentTasks(ctx context.Context, parentConversationID, graphID string) ([]TaskGraphTask, error) {
	var claimed []TaskGraphTask
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		row, graph, err := findTaskGraphOptions(ctx, q, parentConversationID, graphID)
		if err != nil {
			return err
		}
		running := 0
		for _, task := range graph.Tasks {
			if task.Status == "running" {
				running++
			}
		}
		available := max(0, taskGraphMaxConcurrency(graph.MaxConcurrency)-running)
		if available == 0 {
			return nil
		}
		now := tx.Now
		for i := range graph.Tasks {
			task := &graph.Tasks[i]
			if available == 0 {
				break
			}
			if task.Status != "ready" {
				continue
			}
			task.Status = "running"
			task.StartedAt = &now
			graph.UpdatedAt = now
			claimed = append(claimed, *task)
			available--
		}
		if len(claimed) != 0 {
			refreshTaskGraphStatus(graph)
			return saveTaskGraphOptions(ctx, q, row.conversationID, row.opts)
		}
		return nil
	})
	return claimed, err
}

// SetTaskGraphTaskChildConversation atomically associates the claimed task and
// writes the child tag used by completion and recovery.
func (db *DB) SetTaskGraphTaskChildConversation(ctx context.Context, parentConversationID, graphID, taskID, conversationID, slug string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		row, graph, err := findTaskGraphOptions(ctx, q, parentConversationID, graphID)
		if err != nil {
			return err
		}
		task := findTaskGraphTask(graph, taskID)
		if task == nil || task.Status != "running" || task.ChildConversationID != "" {
			return fmt.Errorf("task %q was not available for launch", taskID)
		}
		child, err := q.GetConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		if child.ParentConversationID == nil || *child.ParentConversationID != row.conversationID {
			return fmt.Errorf("task child %q does not belong to parent conversation", conversationID)
		}
		childOpts := ParseConversationOptions(child.ConversationOptions)
		childOpts.TaskGraphChild = &TaskGraphChild{
			ParentConversationID: row.conversationID,
			GraphID:              graphID,
			TaskID:               taskID,
		}
		childOptsJSON, err := json.Marshal(childOpts)
		if err != nil {
			return fmt.Errorf("marshal task graph child options: %w", err)
		}
		task.ChildConversationID = conversationID
		task.Slug = slug
		graph.UpdatedAt = tx.Now
		refreshTaskGraphStatus(graph)
		if err := q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID: conversationID, ConversationOptions: string(childOptsJSON),
		}); err != nil {
			return err
		}
		return saveTaskGraphOptions(ctx, q, row.conversationID, row.opts)
	})
}

// RecoverInterruptedTaskGraphTasks runs at startup. Claimed tasks with no
// child yet go back to ready; tasks whose child was mid-turn fail, since
// subagent turns are not resumed across restarts.
func (db *DB) RecoverInterruptedTaskGraphTasks(ctx context.Context) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		rows, err := listTaskGraphOptions(ctx, tx.Rx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			changed := false
			for i := range row.opts.TaskGraphs {
				graph := &row.opts.TaskGraphs[i]
				for j := range graph.Tasks {
					task := &graph.Tasks[j]
					if task.Status != "running" {
						continue
					}
					if task.ChildConversationID == "" {
						task.Status = "ready"
						task.StartedAt = nil
					} else {
						task.Status = "failed"
						task.Error = "interrupted by server restart"
						now := tx.Now
						task.CompletedAt = &now
					}
					changed = true
				}
				if changed {
					graph.UpdatedAt = tx.Now
					cancelBlockedTaskGraphTasks(graph, tx.Now)
					refreshTaskGraphStatus(graph)
				}
			}
			if changed {
				if err := saveTaskGraphOptions(ctx, q, row.conversationID, row.opts); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// CancelTaskGraph cancels queued or ready tasks. The server handles running
// child conversation cancellation separately.
func (db *DB) CancelTaskGraph(ctx context.Context, parentConversationID, graphID string, taskIDs []string) ([]string, *TaskGraphSnapshot, error) {
	var running []string
	var snapshot TaskGraphSnapshot
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		row, graph, err := findTaskGraphOptions(ctx, q, parentConversationID, graphID)
		if err != nil {
			return err
		}
		changed := false
		if len(taskIDs) == 0 {
			for i := range graph.Tasks {
				task := &graph.Tasks[i]
				if task.Status == "running" {
					running = append(running, task.ID)
					continue
				}
				if task.Status == "pending" || task.Status == "ready" {
					task.Status = "cancelled"
					now := tx.Now
					task.CompletedAt = &now
					changed = true
				}
			}
		} else {
			for _, taskID := range taskIDs {
				task := findTaskGraphTask(graph, taskID)
				if task == nil {
					return sql.ErrNoRows
				}
				if task.Status == "running" {
					running = append(running, taskID)
					continue
				}
				if task.Status == "pending" || task.Status == "ready" {
					task.Status = "cancelled"
					now := tx.Now
					task.CompletedAt = &now
					changed = true
				}
			}
		}
		if changed {
			graph.UpdatedAt = tx.Now
			cancelBlockedTaskGraphTasks(graph, tx.Now)
		}
		refreshTaskGraphStatus(graph)
		if changed {
			if err := saveTaskGraphOptions(ctx, q, row.conversationID, row.opts); err != nil {
				return err
			}
		}
		snapshot = *graph
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return running, &snapshot, nil
}

func (db *DB) CancelRunningTaskGraphTask(ctx context.Context, parentConversationID, graphID, taskID string) (*TaskGraphSnapshot, error) {
	var snapshot TaskGraphSnapshot
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		row, graph, err := findTaskGraphOptions(ctx, q, parentConversationID, graphID)
		if err != nil {
			return err
		}
		task := findTaskGraphTask(graph, taskID)
		if task == nil {
			return sql.ErrNoRows
		}
		if task.Status == "running" {
			task.Status = "cancelled"
			now := tx.Now
			task.CompletedAt = &now
			graph.UpdatedAt = now
			cancelBlockedTaskGraphTasks(graph, now)
			refreshTaskGraphStatus(graph)
			if err := saveTaskGraphOptions(ctx, q, row.conversationID, row.opts); err != nil {
				return err
			}
		}
		refreshTaskGraphStatus(graph)
		snapshot = *graph
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (db *DB) GetTaskGraphChild(ctx context.Context, childConversationID string) (*TaskGraphChild, error) {
	var tag *TaskGraphChild
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		conversation, err := generated.New(rx.Conn()).GetConversation(ctx, childConversationID)
		if err != nil {
			return err
		}
		if stored := ParseConversationOptions(conversation.ConversationOptions).TaskGraphChild; stored != nil {
			copy := *stored
			tag = &copy
		}
		return nil
	})
	return tag, err
}

func listTaskGraphOptions(ctx context.Context, rx *Rx) ([]taskGraphOptionsRow, error) {
	rows, err := rx.Query("SELECT conversation_id, conversation_options FROM conversations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []taskGraphOptionsRow
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		opts := ParseConversationOptions(raw)
		if len(opts.TaskGraphs) != 0 {
			result = append(result, taskGraphOptionsRow{conversationID: id, opts: opts})
		}
	}
	return result, rows.Err()
}

func findTaskGraphOptions(ctx context.Context, q *generated.Queries, parentConversationID, graphID string) (*taskGraphOptionsRow, *TaskGraphSnapshot, error) {
	parent, err := q.GetConversation(ctx, parentConversationID)
	if err != nil {
		return nil, nil, err
	}
	row := &taskGraphOptionsRow{conversationID: parentConversationID, opts: ParseConversationOptions(parent.ConversationOptions)}
	graph := taskGraphByID(&row.opts, graphID)
	if graph == nil {
		return nil, nil, sql.ErrNoRows
	}
	return row, graph, nil
}

func saveTaskGraphOptions(ctx context.Context, q *generated.Queries, conversationID string, opts ConversationOptions) error {
	raw, err := json.Marshal(opts)
	if err != nil {
		return fmt.Errorf("marshal task graph options: %w", err)
	}
	return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
		ConversationID: conversationID, ConversationOptions: string(raw),
	})
}

func taskGraphByID(opts *ConversationOptions, graphID string) *TaskGraphSnapshot {
	for i := range opts.TaskGraphs {
		if opts.TaskGraphs[i].GraphID == graphID {
			return &opts.TaskGraphs[i]
		}
	}
	return nil
}

func findTaskGraphTask(graph *TaskGraphSnapshot, taskID string) *TaskGraphTask {
	for i := range graph.Tasks {
		if graph.Tasks[i].ID == taskID {
			return &graph.Tasks[i]
		}
	}
	return nil
}

func promoteTaskGraphReadyTasks(graph *TaskGraphSnapshot) {
	for i := range graph.Tasks {
		task := &graph.Tasks[i]
		if task.Status != "pending" {
			continue
		}
		ready := true
		for _, dependency := range task.Dependencies {
			prerequisite := findTaskGraphTask(graph, dependency)
			if prerequisite == nil || prerequisite.Status != "complete" {
				ready = false
				break
			}
		}
		if ready {
			task.Status = "ready"
		}
	}
}

func cancelBlockedTaskGraphTasks(graph *TaskGraphSnapshot, now time.Time) {
	for {
		changed := false
		for i := range graph.Tasks {
			task := &graph.Tasks[i]
			if task.Status != "pending" {
				continue
			}
			for _, dependency := range task.Dependencies {
				prerequisite := findTaskGraphTask(graph, dependency)
				if prerequisite != nil && (prerequisite.Status == "cancelled" || prerequisite.Status == "failed") {
					task.Status = "cancelled"
					task.CompletedAt = &now
					changed = true
					break
				}
			}
		}
		if !changed {
			return
		}
	}
}

func refreshTaskGraphStatus(graph *TaskGraphSnapshot) {
	graph.MaxConcurrency = taskGraphMaxConcurrency(graph.MaxConcurrency)
	cancelled, failed, terminal := false, false, true
	for i := range graph.Tasks {
		task := &graph.Tasks[i]
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
		graph.Status = "failed"
	case terminal && cancelled:
		graph.Status = "cancelled"
	case terminal:
		graph.Status = "complete"
	default:
		graph.Status = "active"
	}
}

func taskGraphMaxConcurrency(configured int) int {
	if configured > 0 {
		return configured
	}
	return DefaultTaskGraphMaxConcurrency
}
