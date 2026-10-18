-- Reviewer profiles: a named model configuration a validator step can be bound to (WU-028, A28).
-- The key itself is a secret in the secrets file, named by key_secret.
CREATE TABLE reviewer_profiles (
    name       TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,            -- anthropic or openai
    base_url   TEXT NOT NULL DEFAULT '', -- openai only
    model      TEXT NOT NULL DEFAULT '', -- empty means the kind's default
    tools      TEXT NOT NULL,            -- read-only or full
    key_secret TEXT NOT NULL,            -- secret holding the API key
    updated_at TEXT NOT NULL
);
