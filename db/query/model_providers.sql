-- name: GetModelProviders :many
SELECT * FROM model_providers ORDER BY created_at, provider_id;

-- name: GetModelProvider :one
SELECT * FROM model_providers WHERE provider_id = ?;

-- name: CreateModelProvider :one
INSERT INTO model_providers (
    provider_id, provider_type, endpoint, api_key, display_name
) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateModelProvider :one
UPDATE model_providers
SET provider_type = ?, endpoint = ?, api_key = ?, display_name = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE provider_id = ?
RETURNING *;

-- name: DeleteModelProvider :execrows
DELETE FROM model_providers WHERE provider_id = ?;
