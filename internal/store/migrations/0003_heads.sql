ALTER TABLE runs ADD COLUMN branch TEXT NOT NULL DEFAULT '';
CREATE INDEX runs_by_repo ON runs (repo_id, started_at);

-- Last branch heads seen by the poller.
CREATE TABLE branch_heads (
    repo_id TEXT NOT NULL REFERENCES repos(id),
    branch  TEXT NOT NULL,
    head    TEXT NOT NULL,
    PRIMARY KEY (repo_id, branch)
);

-- Repos the poller has seen at least once.
CREATE TABLE repo_polls (
    repo_id   TEXT PRIMARY KEY REFERENCES repos(id),
    polled_at TEXT NOT NULL
);
