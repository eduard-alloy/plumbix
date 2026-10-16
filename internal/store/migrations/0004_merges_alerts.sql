-- How each merged change landed on its target branch.
CREATE TABLE merges (
    change_id    TEXT PRIMARY KEY REFERENCES changes(id),
    run_id       TEXT NOT NULL REFERENCES runs(id),
    repo_id      TEXT NOT NULL REFERENCES repos(id),
    branch       TEXT NOT NULL,
    commit_sha   TEXT NOT NULL,
    previous_sha TEXT NOT NULL,
    strategy     TEXT NOT NULL,
    merged_at    TEXT NOT NULL
);

CREATE INDEX merges_by_branch ON merges (repo_id, branch, merged_at);

-- Things a human should look at: bypass commits, token expiry.
CREATE TABLE alerts (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    key         TEXT NOT NULL,
    repo_id     TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL,
    details     TEXT NOT NULL, -- JSON object
    created_at  TEXT NOT NULL,
    resolved_at TEXT
);

-- At most one open alert per kind and key.
CREATE UNIQUE INDEX alerts_one_open ON alerts (kind, key) WHERE resolved_at IS NULL;

CREATE INDEX alerts_by_created ON alerts (created_at, id);
