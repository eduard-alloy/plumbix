package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
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
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runs (id, repo_id, change_id, branch, iteration, head, base, status, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.RepoID, changeID, r.Branch, r.Iteration, r.Head, r.Base, string(r.Status), formatTime(r.StartedAt)); err != nil {
			return mapErr(err)
		}
		_, err := emit(events.Event{Type: events.RunStarted, Time: r.StartedAt, RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.ID,
			Payload: events.RunStartedPayload{Branch: r.Branch, Iteration: r.Iteration}})
		return err
	})
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
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var repoID, changeID, branch, head, base string
		var iteration int
		if err := tx.QueryRowContext(ctx,
			`UPDATE runs SET status = ?, outcome = ?, result = ?, error = ?, finished_at = ? WHERE id = ?
			 RETURNING repo_id, COALESCE(change_id, ''), branch, iteration, head, base`,
			string(f.Status), string(f.Outcome), result, f.Error, formatTime(f.At), id).Scan(&repoID, &changeID, &branch, &iteration, &head, &base); err != nil {
			return mapErr(err)
		}
		for i, s := range f.Steps {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO steps (run_id, position, name, validator, status, exit_code, duration_ms, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, s.Name, s.Validator, s.Status, s.ExitCode, s.DurationMS, s.Error); err != nil {
				return mapErr(err)
			}
		}
		if _, err := emit(events.Event{Type: events.RunFinished, Time: f.At, RepoID: repoID, ChangeID: changeID, RunID: id,
			Payload: events.RunFinishedPayload{Branch: branch, Iteration: iteration, Status: string(f.Status), Outcome: string(f.Outcome), Error: f.Error, Head: head, Base: base}}); err != nil {
			return err
		}
		if f.Result == nil {
			return nil
		}
		criteria := make([]events.CriterionStatus, 0, len(f.Result.Criteria))
		for _, c := range f.Result.Criteria {
			criteria = append(criteria, events.CriterionStatus{ID: c.ID, Status: string(c.Status)})
		}
		_, err := emit(events.Event{Type: events.VerdictIssued, Time: f.At, RepoID: repoID, ChangeID: changeID, RunID: id,
			Payload: events.VerdictIssuedPayload{Iteration: iteration, Outcome: string(f.Outcome), Criteria: criteria, Findings: len(f.Result.Findings), Reasons: nonNil(f.Result.Reasons)}})
		return err
	})
}

const runColumns = `id, repo_id, COALESCE(change_id, ''), branch, iteration, head, base, status, outcome, result, error, started_at, finished_at`

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

// ListRepoRuns returns a repo's runs, newest first.
func (db *DB) ListRepoRuns(ctx context.Context, repoID, branch string, limit int) ([]run.Run, error) {
	query := `SELECT ` + runColumns + ` FROM runs WHERE repo_id = ?`
	args := []any{repoID}
	if branch != "" {
		query += ` AND branch = ?`
		args = append(args, branch)
	}
	return db.queryRuns(ctx, query+` ORDER BY started_at DESC, id DESC LIMIT ?`, append(args, limit)...)
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
	if err := s.Scan(&r.ID, &r.RepoID, &r.ChangeID, &r.Branch, &r.Iteration, &r.Head, &r.Base, &status, &outcome, &result, &r.Error, &started, &finished); err != nil {
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
