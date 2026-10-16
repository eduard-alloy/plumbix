package store

import (
	"context"
	"time"
)

// KnownHeads returns the branch heads the poller last saw for a repo, and
// whether the repo was polled before.
func (db *DB) KnownHeads(ctx context.Context, repoID string) (map[string]string, bool, error) {
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_polls WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
		return nil, false, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT branch, head FROM branch_heads WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	heads := map[string]string{}
	for rows.Next() {
		var b, h string
		if err := rows.Scan(&b, &h); err != nil {
			return nil, false, err
		}
		heads[b] = h
	}
	return heads, n > 0, rows.Err()
}

// SaveHeads replaces a repo's known branch heads and marks it polled.
func (db *DB) SaveHeads(ctx context.Context, repoID string, heads map[string]string, at time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM branch_heads WHERE repo_id = ?`, repoID); err != nil {
		return err
	}
	for b, h := range heads {
		if _, err := tx.ExecContext(ctx, `INSERT INTO branch_heads (repo_id, branch, head) VALUES (?, ?, ?)`, repoID, b, h); err != nil {
			return mapErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repo_polls (repo_id, polled_at) VALUES (?, ?) ON CONFLICT (repo_id) DO UPDATE SET polled_at = excluded.polled_at`, repoID, formatTime(at)); err != nil {
		return err
	}
	return tx.Commit()
}
