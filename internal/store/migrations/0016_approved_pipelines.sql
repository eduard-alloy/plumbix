-- Approved pipeline files (WU-051, A38): runs on a protected branch get
-- secrets only when their pipeline file's content is approved. change_id is
-- empty for trust on first use.
CREATE TABLE approved_pipelines (
    repo_id     TEXT NOT NULL REFERENCES repos(id),
    path        TEXT NOT NULL,
    digest      TEXT NOT NULL,
    change_id   TEXT NOT NULL DEFAULT '',
    approved_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, path, digest)
);
