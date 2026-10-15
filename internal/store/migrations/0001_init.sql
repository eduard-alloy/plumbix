CREATE TABLE repos (
    id                 TEXT PRIMARY KEY,
    host               TEXT NOT NULL,
    url                TEXT NOT NULL UNIQUE,
    protected_branches TEXT NOT NULL, -- JSON array
    merge_strategy     TEXT NOT NULL,
    created_at         TEXT NOT NULL
);

CREATE TABLE changes (
    id                  TEXT PRIMARY KEY,
    repo_id             TEXT NOT NULL REFERENCES repos(id),
    source_branch       TEXT NOT NULL,
    target_branch       TEXT NOT NULL,
    title               TEXT NOT NULL,
    intent              TEXT NOT NULL,
    acceptance_criteria TEXT NOT NULL, -- JSON array
    constraints         TEXT NOT NULL, -- JSON array
    submitted_by        TEXT NOT NULL,
    state               TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- At most one change per repo and source branch that is not merged or closed.
CREATE UNIQUE INDEX changes_one_open_per_branch
    ON changes (repo_id, source_branch)
    WHERE state NOT IN ('merged', 'closed');

CREATE INDEX changes_by_created ON changes (created_at, id);
