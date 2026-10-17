-- External validator steps waiting for a verdict from outside the pipeline (WU-023).
CREATE TABLE reviews (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    repo_id     TEXT NOT NULL REFERENCES repos(id),
    change_id   TEXT NOT NULL REFERENCES changes(id),
    step        TEXT NOT NULL,
    status      TEXT NOT NULL,            -- pending, claimed, done, expired
    reviewer    TEXT NOT NULL DEFAULT '',
    token_hash  TEXT NOT NULL,
    brief       TEXT NOT NULL,            -- JSON, the brief without the token
    verdict     TEXT NOT NULL DEFAULT '', -- JSON once posted
    created_at  TEXT NOT NULL,
    claimed_at  TEXT NOT NULL DEFAULT '',
    finished_at TEXT NOT NULL DEFAULT '',
    expires_at  TEXT NOT NULL
);

CREATE INDEX reviews_pending ON reviews (status, created_at);
