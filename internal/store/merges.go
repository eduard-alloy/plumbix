package store

import (
	"context"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// RecordMerge stores how a change landed on its target branch.
func (db *DB) RecordMerge(ctx context.Context, m change.MergeRecord) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO merges (change_id, run_id, repo_id, branch, commit_sha, previous_sha, strategy, merged_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ChangeID, m.RunID, m.RepoID, m.Branch, m.Commit, m.Previous, m.Strategy, formatTime(m.MergedAt))
	return mapErr(err)
}

// ListMerges returns the merges Plumbix pushed to a branch, oldest first.
func (db *DB) ListMerges(ctx context.Context, repoID, branch string) ([]change.MergeRecord, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT change_id, run_id, repo_id, branch, commit_sha, previous_sha, strategy, merged_at FROM merges WHERE repo_id = ? AND branch = ? ORDER BY merged_at, change_id`, repoID, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var merges []change.MergeRecord
	for rows.Next() {
		var m change.MergeRecord
		var at string
		if err := rows.Scan(&m.ChangeID, &m.RunID, &m.RepoID, &m.Branch, &m.Commit, &m.Previous, &m.Strategy, &at); err != nil {
			return nil, err
		}
		if m.MergedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		merges = append(merges, m)
	}
	return merges, rows.Err()
}
