-- name: CreateTaskGraph :one
INSERT INTO task_graphs (graph_id, parent_conversation_id, title)
VALUES (?, ?, ?)
RETURNING *;

-- name: CreateTaskGraphTask :one
INSERT INTO task_graph_tasks (
    graph_id, task_id, position, title, owner, prompt, slug, model, reasoning, file_scopes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateTaskGraphDependency :exec
INSERT INTO task_graph_dependencies (graph_id, task_id, depends_on_task_id)
VALUES (?, ?, ?);

-- name: GetTaskGraph :one
SELECT * FROM task_graphs
WHERE graph_id = ?;

-- name: GetLatestTaskGraph :one
SELECT * FROM task_graphs
WHERE parent_conversation_id = ?
ORDER BY rowid DESC
LIMIT 1;

-- name: ListTaskGraphsWithReadySubagentTasks :many
SELECT DISTINCT graph_id
FROM task_graph_tasks
WHERE owner = 'subagent' AND status = 'ready'
ORDER BY graph_id;

-- name: ListTaskGraphTasks :many
SELECT * FROM task_graph_tasks
WHERE graph_id = ?
ORDER BY position;

-- name: ListTaskGraphDependencies :many
SELECT * FROM task_graph_dependencies
WHERE graph_id = ?
ORDER BY task_id, depends_on_task_id;

-- name: GetTaskGraphTask :one
SELECT * FROM task_graph_tasks
WHERE graph_id = ? AND task_id = ?;

-- name: PromoteTaskGraphReadyTasks :execrows
UPDATE task_graph_tasks
SET status = 'ready'
WHERE graph_id = ?
  AND status = 'queued'
  AND NOT EXISTS (
      SELECT 1
      FROM task_graph_dependencies d
      JOIN task_graph_tasks prerequisite
        ON prerequisite.graph_id = d.graph_id
       AND prerequisite.task_id = d.depends_on_task_id
      WHERE d.graph_id = task_graph_tasks.graph_id
        AND d.task_id = task_graph_tasks.task_id
        AND prerequisite.status != 'complete'
  );

-- name: ClaimReadyTaskGraphSubagentTasks :many
UPDATE task_graph_tasks
SET status = 'running',
    started_at = CURRENT_TIMESTAMP
WHERE task_graph_tasks.graph_id = ?1
  AND task_graph_tasks.task_id IN (
      SELECT task_id
      FROM task_graph_tasks
      WHERE graph_id = ?1
        AND owner = 'subagent'
        AND status = 'ready'
      ORDER BY position
      LIMIT (
          SELECT MAX(0, 3 - COUNT(*))
          FROM task_graph_tasks
          WHERE graph_id = ?1
            AND owner = 'subagent'
            AND status = 'running'
      )
  )
RETURNING *;

-- name: SetTaskGraphTaskChildConversation :execrows
UPDATE task_graph_tasks
SET child_conversation_id = ?,
    slug = ?
WHERE graph_id = ? AND task_id = ?
  AND status = 'running'
  AND child_conversation_id IS NULL;

-- name: ResetUnstartedTaskGraphTasks :execrows
UPDATE task_graph_tasks
SET status = 'ready',
    started_at = NULL
WHERE status = 'running'
  AND child_conversation_id IS NULL;

-- name: CompleteParentTaskGraphTask :execrows
UPDATE task_graph_tasks
SET status = 'complete',
    final_response = ?,
    completed_at = CURRENT_TIMESTAMP
WHERE graph_id = ? AND task_id = ?
  AND owner = 'parent'
  AND status = 'ready';

-- name: CompleteChildTaskGraphTask :execrows
UPDATE task_graph_tasks
SET status = 'complete',
    final_response = ?,
    completed_at = CURRENT_TIMESTAMP
WHERE child_conversation_id = ?
  AND owner = 'subagent'
  AND status = 'running';

-- name: FailTaskGraphTask :execrows
UPDATE task_graph_tasks
SET status = 'failed',
    final_response = ?,
    completed_at = CURRENT_TIMESTAMP
WHERE graph_id = ? AND task_id = ?
  AND status = 'running';


-- name: FindTaskGraphTaskByChildConversation :one
SELECT * FROM task_graph_tasks
WHERE child_conversation_id = ?;

-- name: CancelReadyTaskGraphTask :execrows
UPDATE task_graph_tasks
SET status = 'cancelled',
    completed_at = CURRENT_TIMESTAMP
WHERE graph_id = ? AND task_id = ?
  AND status IN ('queued', 'ready');

-- name: CancelRunningTaskGraphTask :execrows
UPDATE task_graph_tasks
SET status = 'cancelled',
    completed_at = CURRENT_TIMESTAMP
WHERE graph_id = ? AND task_id = ?
  AND status = 'running';

-- name: CancelReadyTaskGraphTasks :execrows
UPDATE task_graph_tasks
SET status = 'cancelled',
    completed_at = CURRENT_TIMESTAMP
WHERE graph_id = ?
  AND status IN ('queued', 'ready');

-- name: CancelBlockedTaskGraphTasks :execrows
UPDATE task_graph_tasks
SET status = 'cancelled',
    completed_at = CURRENT_TIMESTAMP
WHERE task_graph_tasks.graph_id = ?
  AND task_graph_tasks.status = 'queued'
  AND EXISTS (
      SELECT 1
      FROM task_graph_dependencies d
      JOIN task_graph_tasks prerequisite
        ON prerequisite.graph_id = d.graph_id
       AND prerequisite.task_id = d.depends_on_task_id
      WHERE d.graph_id = task_graph_tasks.graph_id
        AND d.task_id = task_graph_tasks.task_id
        AND prerequisite.status IN ('cancelled', 'failed')
  );
