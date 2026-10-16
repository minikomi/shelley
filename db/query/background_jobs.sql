-- name: InsertBackgroundJob :exec
INSERT INTO background_jobs (job_id, conversation_id, tool_use_id, command, pid, process_start_time, log_path, exit_path, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListUnnotifiedBackgroundJobs :many
SELECT * FROM background_jobs WHERE NOT notified ORDER BY started_at;

-- name: MarkBackgroundJobNotified :exec
UPDATE background_jobs SET notified = TRUE WHERE job_id = ?;

-- name: MarkBackgroundJobExited :execrows
UPDATE background_jobs SET exited = TRUE WHERE job_id = ? AND NOT exited;

-- name: ListRunningBackgroundJobs :many
SELECT * FROM background_jobs WHERE conversation_id = ? AND NOT exited ORDER BY started_at;

-- name: ListAllRunningBackgroundJobs :many
SELECT * FROM background_jobs WHERE NOT exited ORDER BY started_at;
