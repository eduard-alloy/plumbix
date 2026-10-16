package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
)

var _ alert.Store = (*DB)(nil)

const alertColumns = `id, kind, key, repo_id, message, details, created_at, resolved_at`

// Raise implements alert.Store.
func (db *DB) Raise(ctx context.Context, a alert.Alert) (alert.Alert, bool, error) {
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	details, err := json.Marshal(a.Details)
	if err != nil {
		return alert.Alert{}, false, err
	}
	if a.ID == "" {
		a.ID = change.NewID("al_")
	}
	a.CreatedAt = a.CreatedAt.UTC()
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO alerts (id, kind, key, repo_id, message, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (kind, key) WHERE resolved_at IS NULL DO NOTHING`,
		a.ID, string(a.Kind), a.Key, a.RepoID, a.Message, string(details), formatTime(a.CreatedAt))
	if err != nil {
		return alert.Alert{}, false, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return a, true, nil
	}
	open, err := scanAlert(db.sql.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE kind = ? AND key = ? AND resolved_at IS NULL`, string(a.Kind), a.Key))
	return open, false, err
}

// Resolve implements alert.Store.
func (db *DB) Resolve(ctx context.Context, id string, at time.Time) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE alerts SET resolved_at = COALESCE(resolved_at, ?) WHERE id = ?`, formatTime(at), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: alert %s", change.ErrNotFound, id)
	}
	return nil
}

// ResolveKey implements alert.Store.
func (db *DB) ResolveKey(ctx context.Context, kind alert.Kind, key string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE kind = ? AND key = ? AND resolved_at IS NULL`, formatTime(at), string(kind), key)
	return err
}

// Get implements alert.Store.
func (db *DB) Get(ctx context.Context, id string) (alert.Alert, error) {
	a, err := scanAlert(db.sql.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE id = ?`, id))
	if errors.Is(err, change.ErrNotFound) {
		return alert.Alert{}, fmt.Errorf("%w: alert %s", change.ErrNotFound, id)
	}
	return a, err
}

// List implements alert.Store.
func (db *DB) List(ctx context.Context, all bool) ([]alert.Alert, error) {
	query := `SELECT ` + alertColumns + ` FROM alerts`
	if !all {
		query += ` WHERE resolved_at IS NULL`
	}
	rows, err := db.sql.QueryContext(ctx, query+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := []alert.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

func scanAlert(s scanner) (alert.Alert, error) {
	var a alert.Alert
	var kind, details, created string
	var resolved sql.NullString
	if err := s.Scan(&a.ID, &kind, &a.Key, &a.RepoID, &a.Message, &details, &created, &resolved); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return alert.Alert{}, change.ErrNotFound
		}
		return alert.Alert{}, err
	}
	a.Kind = alert.Kind(kind)
	if err := json.Unmarshal([]byte(details), &a.Details); err != nil {
		return alert.Alert{}, fmt.Errorf("alert %s: details: %w", a.ID, err)
	}
	if len(a.Details) == 0 {
		a.Details = nil
	}
	var err error
	if a.CreatedAt, err = parseTime(created); err != nil {
		return alert.Alert{}, err
	}
	if resolved.Valid {
		t, err := parseTime(resolved.String)
		if err != nil {
			return alert.Alert{}, err
		}
		a.ResolvedAt = &t
	}
	return a, nil
}
