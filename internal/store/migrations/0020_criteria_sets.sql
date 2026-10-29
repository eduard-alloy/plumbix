-- WU-066 (A43): acceptance criteria written with the specification, before
-- the code; a change names a set and takes its criteria frozen.
CREATE TABLE criteria_sets (
    id         TEXT PRIMARY KEY,
    task       TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    owner      TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT '',
    criteria   TEXT NOT NULL,
    frozen     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX criteria_sets_by_task ON criteria_sets (task, created_at);
ALTER TABLE changes ADD COLUMN cs_id TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN cs_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN cs_task TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN cs_problem TEXT NOT NULL DEFAULT '';
CREATE INDEX changes_by_criteria_set ON changes (cs_id) WHERE cs_id <> '';
