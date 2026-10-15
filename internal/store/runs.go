package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var _ run.Store = (*DB)(nil)

// CreateRun stores a new run.
func (db *DB) CreateRun(ctx context.Context, r run.Run) error {
	var changeID any
	if r.ChangeID != "" {
		changeID = r.ChangeID
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO runs (id, repo_id, change_id, iteration, head, base, status, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.RepoID, changeID, r.Iteration, r.Head, r.Base, string(r.Status), formatTime(r.StartedAt))
	return mapErr(err)
}

// SetRunCommits records the commits a run compares.
func (db *DB) SetRunCommits(ctx context.Context, id, head, base string) error {
	return db.updateOne(ctx, `UPDATE runs SET head = ?, base = ? WHERE id = ?`, head, base, id)
}

// FinishRun records a run's end, its steps and its combined result.
func (db *DB) FinishRun(ctx context.Context, id string, f run.Finish) error {
	result := ""
	if f.Result != nil {
		data, err := json.Marshal(f.Result)
		if err != nil {
			return err
		}
		result = string(data)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, outcome = ?, result = ?, error = ?, finished_at = ? WHERE id = ?`,
		string(f.Status), string(f.Outcome), result, f.Error, formatTime(f.At), id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return change.ErrNotFound
	}
	for i, s := range f.Steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO steps (run_id, position, name, validator, status, exit_code, duration_ms, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, s.Name, s.Validator, s.Status, s.ExitCode, s.DurationMS, s.Error); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

const runColumns = `id, repo_id, COALESCE(change_id, ''), iteration, head, base, status, outcome, result, error, started_at, finished_at`

// GetRun returns one run with its steps.
func (db *DB) GetRun(ctx context.Context, id string) (run.Run, error) {
	r, err := scanRun(db.sql.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if err != nil {
		return run.Run{}, err
	}
	return db.withSteps(ctx, r)
}

// ListRuns returns a change's runs by iteration.
func (db *DB) ListRuns(ctx context.Context, changeID string) ([]run.Run, error) {
	return db.queryRuns(ctx, `SELECT `+runColumns+` FROM runs WHERE change_id = ? ORDER BY iteration`, changeID)
}

// RunningRuns returns runs still marked running.
func (db *DB) RunningRuns(ctx context.Context) ([]run.Run, error) {
	return db.queryRuns(ctx, `SELECT `+runColumns+` FROM runs WHERE status = ? ORDER BY started_at, id`, string(run.Running))
}

// LastIteration returns the highest iteration number of a change, or 0.
func (db *DB) LastIteration(ctx context.Context, changeID string) (int, error) {
	var n sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `SELECT MAX(iteration) FROM runs WHERE change_id = ?`, changeID).Scan(&n)
	return int(n.Int64), err
}

func (db *DB) queryRuns(ctx context.Context, query string, args ...any) ([]run.Run, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var runs []run.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []run.Run{}
	for _, r := range runs {
		r, err := db.withSteps(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (db *DB) withSteps(ctx context.Context, r run.Run) (run.Run, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name, validator, status, exit_code, duration_ms, error FROM steps WHERE run_id = ? ORDER BY position`, r.ID)
	if err != nil {
		return run.Run{}, err
	}
	defer rows.Close()
	r.Steps = []run.Step{}
	for rows.Next() {
		var s run.Step
		if err := rows.Scan(&s.Name, &s.Validator, &s.Status, &s.ExitCode, &s.DurationMS, &s.Error); err != nil {
			return run.Run{}, err
		}
		r.Steps = append(r.Steps, s)
	}
	return r, rows.Err()
}

func scanRun(s scanner) (run.Run, error) {
	var r run.Run
	var status, outcome, result, started, finished string
	if err := s.Scan(&r.ID, &r.RepoID, &r.ChangeID, &r.Iteration, &r.Head, &r.Base, &status, &outcome, &result, &r.Error, &started, &finished); err != nil {
		return run.Run{}, mapErr(err)
	}
	r.Status, r.Outcome = run.Status(status), change.State(outcome)
	if result != "" {
		r.Result = &validation.Result{}
		if err := json.Unmarshal([]byte(result), r.Result); err != nil {
			return run.Run{}, fmt.Errorf("run %s: result: %w", r.ID, err)
		}
	}
	var err error
	if r.StartedAt, err = parseTime(started); err != nil {
		return run.Run{}, err
	}
	if finished != "" {
		t, err := parseTime(finished)
		if err != nil {
			return run.Run{}, err
		}
		r.FinishedAt = &t
	}
	return r, nil
}

func (db *DB) updateOne(ctx context.Context, query string, args ...any) error {
	res, err := db.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return change.ErrNotFound
	}
	return nil
}
