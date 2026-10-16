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
	"github.com/eduard-alloy/plumbix/internal/events"
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
	created := false
	err = db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO alerts (id, kind, key, repo_id, message, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (kind, key) WHERE resolved_at IS NULL DO NOTHING`,
			a.ID, string(a.Kind), a.Key, a.RepoID, a.Message, string(details), formatTime(a.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil
		}
		created = true
		_, err = emit(events.Event{Type: events.AlertRaised, Time: a.CreatedAt, RepoID: a.RepoID,
			Payload: events.AlertRaisedPayload{AlertID: a.ID, Kind: string(a.Kind), Key: a.Key, Message: a.Message}})
		return err
	})
	if err != nil {
		return alert.Alert{}, false, err
	}
	if created {
		return a, true, nil
	}
	open, err := scanAlert(db.sql.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE kind = ? AND key = ? AND resolved_at IS NULL`, string(a.Kind), a.Key))
	return open, false, err
}

// Resolve implements alert.Store.
func (db *DB) Resolve(ctx context.Context, id string, at time.Time) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var kind, key, repoID string
		err := tx.QueryRowContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL RETURNING kind, key, repo_id`,
			formatTime(at), id).Scan(&kind, &key, &repoID)
		if errors.Is(err, sql.ErrNoRows) {
			// Already resolved, or no such alert.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE id = ?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("%w: alert %s", change.ErrNotFound, id)
			}
			return nil
		}
		if err != nil {
			return err
		}
		_, err = emit(events.Event{Type: events.AlertResolved, Time: at, RepoID: repoID,
			Payload: events.AlertResolvedPayload{AlertID: id, Kind: kind, Key: key}})
		return err
	})
}

// ResolveKey implements alert.Store.
func (db *DB) ResolveKey(ctx context.Context, kind alert.Kind, key string, at time.Time) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		rows, err := tx.QueryContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE kind = ? AND key = ? AND resolved_at IS NULL RETURNING id, repo_id`,
			formatTime(at), string(kind), key)
		if err != nil {
			return err
		}
		type resolved struct{ id, repo string }
		var all []resolved
		for rows.Next() {
			var r resolved
			if err := rows.Scan(&r.id, &r.repo); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range all {
			if _, err := emit(events.Event{Type: events.AlertResolved, Time: at, RepoID: r.repo,
				Payload: events.AlertResolvedPayload{AlertID: r.id, Kind: string(kind), Key: key}}); err != nil {
				return err
			}
		}
		return nil
	})
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
