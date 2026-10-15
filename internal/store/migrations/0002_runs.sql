CREATE TABLE runs (
    id          TEXT PRIMARY KEY,
    repo_id     TEXT NOT NULL REFERENCES repos(id),
    change_id   TEXT REFERENCES changes(id),
    iteration   INTEGER NOT NULL DEFAULT 0,
    head        TEXT NOT NULL DEFAULT '',
    base        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    outcome     TEXT NOT NULL DEFAULT '',
    result      TEXT NOT NULL DEFAULT '', -- JSON, empty when none
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX runs_one_iteration_per_change ON runs (change_id, iteration) WHERE change_id IS NOT NULL;
CREATE INDEX runs_by_status ON runs (status);

CREATE TABLE steps (
    run_id      TEXT NOT NULL REFERENCES runs(id),
    position    INTEGER NOT NULL,
    name        TEXT NOT NULL,
    validator   INTEGER NOT NULL,
    status      TEXT NOT NULL,
    exit_code   INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, name)
);
