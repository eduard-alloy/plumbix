package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/eduard-alloy/plumbix/internal/guard"
)

var _ guard.Store = (*DB)(nil)

// ApprovePipelines implements guard.Store.
func (db *DB) ApprovePipelines(ctx context.Context, repoID string, files []guard.Approval, changeID string, at time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO approved_pipelines (repo_id, path, digest, change_id, approved_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			repoID, f.Path, f.Digest, changeID, formatTime(at)); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// PipelineApproved implements guard.Store.
func (db *DB) PipelineApproved(ctx context.Context, repoID, path, digest string) (bool, error) {
	var one int
	err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM approved_pipelines WHERE repo_id = ? AND path = ? AND digest = ?`, repoID, path, digest).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// HasPipelineApprovals implements guard.Store.
func (db *DB) HasPipelineApprovals(ctx context.Context, repoID string) (bool, error) {
	var one int
	err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM approved_pipelines WHERE repo_id = ? LIMIT 1`, repoID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
