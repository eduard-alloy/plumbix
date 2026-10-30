-- WU-065: a removed repository keeps its URL for the record, so the URL is
-- unique among live registrations only. SQLite cannot drop a column
-- constraint, so the table is rebuilt under its own name: the rows go to a
-- temporary table, the table is dropped and recreated, the rows come back.
-- Foreign keys from the children are deferred to the commit, and the rows
-- reinserted under the name "repos" settle them before it.
PRAGMA defer_foreign_keys = ON;
CREATE TEMP TABLE repos_backup AS SELECT id, host, url, protected_branches, merge_strategy, created_at, pull_requests, merger, paused, removed_at, removed_by FROM repos;
DROP TABLE repos;
CREATE TABLE repos (
    id                 TEXT PRIMARY KEY,
    host               TEXT NOT NULL,
    url                TEXT NOT NULL,
    protected_branches TEXT NOT NULL,
    merge_strategy     TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    pull_requests      INTEGER NOT NULL DEFAULT 0,
    merger             TEXT NOT NULL DEFAULT 'plumbix',
    paused             INTEGER NOT NULL DEFAULT 0,
    removed_at         TEXT NOT NULL DEFAULT '',
    removed_by         TEXT NOT NULL DEFAULT ''
);
INSERT INTO repos (id, host, url, protected_branches, merge_strategy, created_at, pull_requests, merger, paused, removed_at, removed_by)
    SELECT id, host, url, protected_branches, merge_strategy, created_at, pull_requests, merger, paused, removed_at, removed_by FROM repos_backup;
DROP TABLE repos_backup;
CREATE UNIQUE INDEX repos_live_url ON repos (url) WHERE removed_at = '';
