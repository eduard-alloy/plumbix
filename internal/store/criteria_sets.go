package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// Criteria sets (WU-066): criteria written with the specification, which a
// change names and copies at submission. A set a change has used is frozen.

func (db *DB) CreateCriteriaSet(ctx context.Context, cs change.CriteriaSet) error {
	criteria, source, err := encodeCriteriaSet(cs)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx, `INSERT INTO criteria_sets (id, task, title, owner, source, criteria, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cs.ID, cs.Task, cs.Title, cs.Owner, source, criteria, formatTime(cs.CreatedAt), formatTime(cs.UpdatedAt))
	return mapErr(err)
}

// UpdateCriteriaSet replaces a set that no change has used yet; the check
// and the write share one transaction, so a submission in between cannot
// slip past it.
func (db *DB) UpdateCriteriaSet(ctx context.Context, cs change.CriteriaSet) error {
	criteria, source, err := encodeCriteriaSet(cs)
	if err != nil {
		return err
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var frozen, used int
	if err := tx.QueryRowContext(ctx, `SELECT frozen, (SELECT COUNT(*) FROM changes WHERE cs_id = criteria_sets.id) FROM criteria_sets WHERE id = ?`, cs.ID).Scan(&frozen, &used); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: criteria set %s", change.ErrNotFound, cs.ID)
		}
		return mapErr(err)
	}
	if frozen > 0 || used > 0 {
		return fmt.Errorf("%w: set %s", change.ErrCriteriaSetUsed, cs.ID)
	}
	res, err := tx.ExecContext(ctx, `UPDATE criteria_sets SET task = ?, title = ?, owner = ?, source = ?, criteria = ?, updated_at = ? WHERE id = ?`,
		cs.Task, cs.Title, cs.Owner, source, criteria, formatTime(cs.UpdatedAt), cs.ID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: criteria set %s", change.ErrNotFound, cs.ID)
	}
	return tx.Commit()
}

func (db *DB) GetCriteriaSet(ctx context.Context, id string) (change.CriteriaSet, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT id, task, title, owner, source, criteria, frozen, created_at, updated_at FROM criteria_sets WHERE id = ?`, id)
	cs, err := scanCriteriaSet(row)
	if err != nil {
		return change.CriteriaSet{}, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT id FROM changes WHERE cs_id = ? ORDER BY created_at, id`, id)
	if err != nil {
		return change.CriteriaSet{}, mapErr(err)
	}
	defer rows.Close()
	cs.UsedBy = []string{}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return change.CriteriaSet{}, err
		}
		cs.UsedBy = append(cs.UsedBy, cid)
	}
	return cs, rows.Err()
}

// ListCriteriaSets lists sets newest first, those of one task when task is
// given; UsedBy is filled for each.
func (db *DB) ListCriteriaSets(ctx context.Context, task string) ([]change.CriteriaSet, error) {
	q := `SELECT id, task, title, owner, source, criteria, frozen, created_at, updated_at FROM criteria_sets`
	var args []any
	if task != "" {
		q += ` WHERE task = ?`
		args = append(args, task)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 500`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []change.CriteriaSet{}
	for rows.Next() {
		cs, err := scanCriteriaSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		full, err := db.GetCriteriaSet(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].UsedBy = full.UsedBy
	}
	return out, nil
}

func encodeCriteriaSet(cs change.CriteriaSet) (criteria, source string, err error) {
	c, err := json.Marshal(cs.Criteria)
	if err != nil {
		return "", "", err
	}
	if cs.Source == nil {
		return string(c), "", nil
	}
	s, err := json.Marshal(cs.Source)
	if err != nil {
		return "", "", err
	}
	return string(c), string(s), nil
}

func scanCriteriaSet(s scanner) (change.CriteriaSet, error) {
	var cs change.CriteriaSet
	var source, criteria, created, updated string
	if err := s.Scan(&cs.ID, &cs.Task, &cs.Title, &cs.Owner, &source, &criteria, &cs.Frozen, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cs, change.ErrNotFound
		}
		return cs, mapErr(err)
	}
	if err := json.Unmarshal([]byte(criteria), &cs.Criteria); err != nil {
		return cs, fmt.Errorf("criteria set %s: criteria: %w", cs.ID, err)
	}
	if source != "" {
		cs.Source = &change.CriteriaSource{}
		if err := json.Unmarshal([]byte(source), cs.Source); err != nil {
			return cs, fmt.Errorf("criteria set %s: source: %w", cs.ID, err)
		}
	}
	var err error
	if cs.CreatedAt, err = parseTime(created); err != nil {
		return cs, err
	}
	if cs.UpdatedAt, err = parseTime(updated); err != nil {
		return cs, err
	}
	cs.UsedBy = []string{}
	return cs, nil
}
