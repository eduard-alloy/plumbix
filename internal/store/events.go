package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/eduard-alloy/plumbix/internal/events"
)

var _ events.Store = (*DB)(nil)

// OnEvent sets the function that receives every event after its
// transaction committed, in id order. Set it once, before writes start.
func (db *DB) OnEvent(fn func(events.Event)) {
	db.emitMu.Lock()
	defer db.emitMu.Unlock()
	db.notify = fn
}

// AppendEvent implements events.Appender, for facts no other write records.
func (db *DB) AppendEvent(ctx context.Context, e events.Event) (events.Event, error) {
	var out events.Event
	err := db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var err error
		out, err = emit(e)
		return err
	})
	return out, err
}

// EventsAfter implements events.Reader.
func (db *DB) EventsAfter(ctx context.Context, after int64, f events.Filter, limit int) ([]events.Event, error) {
	query := `SELECT id, type, time, repo_id, change_id, run_id, payload FROM events WHERE id > ?`
	args := []any{after}
	if f.RepoID != "" {
		query += ` AND repo_id = ?`
		args = append(args, f.RepoID)
	}
	if f.ChangeID != "" {
		query += ` AND change_id = ?`
		args = append(args, f.ChangeID)
	}
	rows, err := db.sql.QueryContext(ctx, query+` ORDER BY id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []events.Event{}
	for rows.Next() {
		var e events.Event
		var typ, at, payload string
		if err := rows.Scan(&e.ID, &typ, &at, &e.RepoID, &e.ChangeID, &e.RunID, &payload); err != nil {
			return nil, err
		}
		e.Type = events.Type(typ)
		if e.Time, err = parseTime(at); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// emitFunc records an event inside the current transaction.
type emitFunc func(events.Event) (events.Event, error)

// writeTx runs fn in a transaction. Events fn emits are inserted in that
// same transaction and, once it commits, handed to the OnEvent function.
// One writeTx runs at a time, so ids are published in order and a live
// subscriber never sees a newer event before an older one.
func (db *DB) writeTx(ctx context.Context, fn func(tx *sql.Tx, emit emitFunc) error) error {
	db.emitMu.Lock()
	defer db.emitMu.Unlock()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var queued []events.Event
	emit := func(e events.Event) (events.Event, error) {
		e, err := insertEvent(ctx, tx, e)
		if err == nil {
			queued = append(queued, e)
		}
		return e, err
	}
	if err := fn(tx, emit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if db.notify != nil {
		for _, e := range queued {
			db.notify(e)
		}
	}
	return nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, e events.Event) (events.Event, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	if e.Payload == nil {
		e.Payload = map[string]any{}
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return e, err
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO events (type, time, repo_id, change_id, run_id, payload) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		string(e.Type), formatTime(e.Time), e.RepoID, e.ChangeID, e.RunID, string(payload)).Scan(&e.ID)
	return e, err
}
