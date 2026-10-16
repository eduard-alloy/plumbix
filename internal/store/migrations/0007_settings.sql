-- Operator settings: review guidance, the AI model, and when each secret
-- was last set. Secret values themselves live in the secrets file.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
