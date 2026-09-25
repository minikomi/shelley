-- Transcription models are separate from chat models. Custom rows are durable;
-- managed integration models are supplied at runtime and share the same
-- provider-neutral catalog/API shape.
CREATE TABLE transcription_models (
    model_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol IN ('openai', 'deepgram')),
    provider TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',
    model_name TEXT NOT NULL,
    supports_prompted BOOLEAN NOT NULL DEFAULT FALSE,
    supports_timecodes BOOLEAN NOT NULL DEFAULT FALSE,
    managed BOOLEAN NOT NULL DEFAULT FALSE,
    source TEXT NOT NULL DEFAULT 'custom',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    api_profile TEXT NOT NULL DEFAULT 'auto'
        CHECK (api_profile IN ('auto', 'openai-json', 'openai-whisper', 'openai-diarized')),
    request_encoding TEXT NOT NULL DEFAULT 'auto'
        CHECK (request_encoding IN ('auto', 'multipart', 'base64-json')),
    CHECK (protocol != 'deepgram' OR supports_prompted = FALSE)
);
