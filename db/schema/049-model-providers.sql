-- Saved provider connection metadata and credentials. Discovered models remain
-- runtime data and are not persisted here.
CREATE TABLE model_providers (
    provider_id TEXT PRIMARY KEY,
    provider_type TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    api_key TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
