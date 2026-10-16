-- What Plumbix announces: one row per fact, in the order it happened.
-- AUTOINCREMENT keeps ids growing and never reused, so a client that
-- resumes from the last id it saw misses nothing.
CREATE TABLE events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    type      TEXT NOT NULL,
    time      TEXT NOT NULL,
    repo_id   TEXT NOT NULL DEFAULT '',
    change_id TEXT NOT NULL DEFAULT '',
    run_id    TEXT NOT NULL DEFAULT '',
    payload   TEXT NOT NULL -- JSON object
);

CREATE INDEX events_by_repo ON events (repo_id, id);
CREATE INDEX events_by_change ON events (change_id, id);
