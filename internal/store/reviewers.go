package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// ReviewerProfile is one row of reviewer_profiles (WU-028). The settings
// package validates and interprets it; the store only keeps it.
type ReviewerProfile struct {
	Name      string
	Kind      string
	BaseURL   string
	Model     string
	Tools     string
	KeySecret string
	UpdatedAt time.Time
}

const reviewerProfileColumns = `name, kind, base_url, model, tools, key_secret, updated_at`

// PutReviewerProfile stores or replaces a profile by name.
func (db *DB) PutReviewerProfile(ctx context.Context, p ReviewerProfile) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO reviewer_profiles (`+reviewerProfileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, base_url = excluded.base_url, model = excluded.model,
		   tools = excluded.tools, key_secret = excluded.key_secret, updated_at = excluded.updated_at`,
		p.Name, p.Kind, p.BaseURL, p.Model, p.Tools, p.KeySecret, formatTime(p.UpdatedAt))
	return mapErr(err)
}

// GetReviewerProfile returns one profile, or change.ErrNotFound.
func (db *DB) GetReviewerProfile(ctx context.Context, name string) (ReviewerProfile, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT `+reviewerProfileColumns+` FROM reviewer_profiles WHERE name = ?`, name)
	return scanReviewerProfile(row)
}

// ListReviewerProfiles returns every profile, by name.
func (db *DB) ListReviewerProfiles(ctx context.Context) ([]ReviewerProfile, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+reviewerProfileColumns+` FROM reviewer_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewerProfile
	for rows.Next() {
		p, err := scanReviewerProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteReviewerProfile removes a profile, or returns change.ErrNotFound.
func (db *DB) DeleteReviewerProfile(ctx context.Context, name string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM reviewer_profiles WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return change.ErrNotFound
	}
	return nil
}

func scanReviewerProfile(row interface{ Scan(...any) error }) (ReviewerProfile, error) {
	var p ReviewerProfile
	var at string
	if err := row.Scan(&p.Name, &p.Kind, &p.BaseURL, &p.Model, &p.Tools, &p.KeySecret, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReviewerProfile{}, change.ErrNotFound
		}
		return ReviewerProfile{}, err
	}
	var err error
	p.UpdatedAt, err = parseTime(at)
	return p, err
}
