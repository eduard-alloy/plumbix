-- Pull requests as changes (WU-054): a repository opts into pull-request
-- intake and names who merges; a change remembers the pull request it
-- mirrors; a merge GitHub made may carry no run and records who merged.
ALTER TABLE repos ADD COLUMN pull_requests INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repos ADD COLUMN merger TEXT NOT NULL DEFAULT 'plumbix';
ALTER TABLE changes ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE changes ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';

CREATE TABLE merges_new (
    change_id    TEXT PRIMARY KEY REFERENCES changes(id),
    run_id       TEXT REFERENCES runs(id),
    repo_id      TEXT NOT NULL REFERENCES repos(id),
    branch       TEXT NOT NULL,
    commit_sha   TEXT NOT NULL,
    previous_sha TEXT NOT NULL,
    strategy     TEXT NOT NULL,
    merged_by    TEXT NOT NULL DEFAULT '',
    merged_at    TEXT NOT NULL
);
INSERT INTO merges_new (change_id, run_id, repo_id, branch, commit_sha, previous_sha, strategy, merged_at)
    SELECT change_id, run_id, repo_id, branch, commit_sha, previous_sha, strategy, merged_at FROM merges;
DROP TABLE merges;
ALTER TABLE merges_new RENAME TO merges;
CREATE INDEX merges_by_branch ON merges (repo_id, branch, merged_at);
CREATE INDEX changes_by_pull_request ON changes (repo_id, pr_number) WHERE pr_number > 0;
