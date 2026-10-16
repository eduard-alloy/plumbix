package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Setting is one stored value with the time it was last set.
type Setting struct {
	Value     string
	UpdatedAt time.Time
}

// GetSetting returns one setting; ok is false when it is not set.
func (db *DB) GetSetting(ctx context.Context, key string) (string, time.Time, bool, error) {
	var value, at string
	err := db.sql.QueryRowContext(ctx, `SELECT value, updated_at FROM settings WHERE key = ?`, key).Scan(&value, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	when, err := parseTime(at)
	return value, when, err == nil, err
}

// SetSetting stores or replaces a setting.
func (db *DB) SetSetting(ctx context.Context, key, value string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTime(at))
	return err
}

// DeleteSetting removes a setting; a missing one is not an error.
func (db *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// ListSettings returns every setting whose key starts with prefix.
func (db *DB) ListSettings(ctx context.Context, prefix string) (map[string]Setting, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT key, value, updated_at FROM settings WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Setting{}
	for rows.Next() {
		var key, value, at string
		if err := rows.Scan(&key, &value, &at); err != nil {
			return nil, err
		}
		when, err := parseTime(at)
		if err != nil {
			return nil, err
		}
		out[key] = Setting{Value: value, UpdatedAt: when}
	}
	return out, rows.Err()
}
