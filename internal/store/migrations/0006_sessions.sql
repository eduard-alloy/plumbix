-- Browser sign-in. Tokens are never stored, only their SHA-256 hashes.
CREATE TABLE login_links (
    hash       TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);

CREATE TABLE sessions (
    hash       TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
