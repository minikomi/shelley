-- name: ListTranscriptionModels :many
SELECT * FROM transcription_models ORDER BY created_at ASC, model_id ASC;

-- name: GetTranscriptionModel :one
SELECT * FROM transcription_models WHERE model_id = ?;

-- name: CreateTranscriptionModel :one
INSERT INTO transcription_models (
    model_id, display_name, protocol, provider, endpoint, api_key, model_name,
    api_profile, request_encoding, supports_prompted, supports_timecodes, managed, source
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateTranscriptionModel :one
UPDATE transcription_models
SET display_name = ?,
    protocol = ?,
    provider = ?,
    endpoint = ?,
    api_key = ?,
    model_name = ?,
    api_profile = ?,
    request_encoding = ?,
    supports_prompted = ?,
    supports_timecodes = ?,
    source = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE model_id = ? AND managed = FALSE
RETURNING *;

-- name: DeleteTranscriptionModel :execrows
DELETE FROM transcription_models WHERE model_id = ? AND managed = FALSE;

-- name: ListTranscriptionModelDefaults :many
SELECT key, value
FROM settings
WHERE key IN ('transcription.default.transcript', 'transcription.default.timecoded')
ORDER BY key ASC;

-- name: InitializeTranscriptionModelDefault :execrows
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO NOTHING;

-- name: SetTranscriptionModelDefault :exec
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteTranscriptionModelDefault :execrows
DELETE FROM settings WHERE key = ?;
