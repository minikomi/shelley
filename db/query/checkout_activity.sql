-- name: RecordCheckoutActivity :exec
INSERT INTO checkout_activity (conversation_id, machine, worktree, touched_at, edited)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (conversation_id, machine, worktree) DO UPDATE SET
    touched_at = max(touched_at, excluded.touched_at),
    edited = edited OR excluded.edited;

-- name: RecentCheckouts :many
WITH RECURSIVE chats(id) AS (
    SELECT CAST(sqlc.arg(chat) AS TEXT)
    UNION ALL
    SELECT c.conversation_id FROM conversations c JOIN chats ON c.parent_conversation_id = chats.id
)
SELECT a.worktree FROM checkout_activity a JOIN chats ON chats.id = a.conversation_id
WHERE a.machine = sqlc.arg(machine) AND (a.edited OR (a.worktree = sqlc.arg(workspace) AND a.conversation_id = sqlc.arg(chat)))
GROUP BY a.worktree
ORDER BY max(a.touched_at) DESC, a.worktree;
