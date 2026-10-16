package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/eduard-alloy/plumbix/internal/auth"
)

var _ auth.SessionStore = (*DB)(nil)

// CreateLoginLink implements auth.SessionStore.
func (db *DB) CreateLoginLink(ctx context.Context, l auth.LoginLink) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO login_links (hash, created_at, expires_at) VALUES (?, ?, ?)`,
		l.Hash, formatTime(l.CreatedAt), formatTime(l.ExpiresAt))
	return mapErr(err)
}

// GetLoginLink implements auth.SessionStore.
func (db *DB) GetLoginLink(ctx context.Context, hash string) (auth.LoginLink, bool, error) {
	var l auth.LoginLink
	var created, expires string
	var used sql.NullString
	err := db.sql.QueryRowContext(ctx, `SELECT hash, created_at, expires_at, used_at FROM login_links WHERE hash = ?`, hash).Scan(&l.Hash, &created, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.LoginLink{}, false, nil
	}
	if err != nil {
		return auth.LoginLink{}, false, err
	}
	if l.CreatedAt, err = parseTime(created); err != nil {
		return auth.LoginLink{}, false, err
	}
	if l.ExpiresAt, err = parseTime(expires); err != nil {
		return auth.LoginLink{}, false, err
	}
	if used.Valid {
		t, err := parseTime(used.String)
		if err != nil {
			return auth.LoginLink{}, false, err
		}
		l.UsedAt = &t
	}
	return l, true, nil
}

// UseLoginLink implements auth.SessionStore.
func (db *DB) UseLoginLink(ctx context.Context, hash string, at time.Time) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `UPDATE login_links SET used_at = ? WHERE hash = ? AND used_at IS NULL`, formatTime(at), hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CreateSession implements auth.SessionStore.
func (db *DB) CreateSession(ctx context.Context, s auth.Session) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO sessions (hash, created_at, expires_at) VALUES (?, ?, ?)`,
		s.Hash, formatTime(s.CreatedAt), formatTime(s.ExpiresAt))
	return mapErr(err)
}

// GetSession implements auth.SessionStore.
func (db *DB) GetSession(ctx context.Context, hash string) (auth.Session, bool, error) {
	var s auth.Session
	var created, expires string
	err := db.sql.QueryRowContext(ctx, `SELECT hash, created_at, expires_at FROM sessions WHERE hash = ?`, hash).Scan(&s.Hash, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, false, nil
	}
	if err != nil {
		return auth.Session{}, false, err
	}
	if s.CreatedAt, err = parseTime(created); err != nil {
		return auth.Session{}, false, err
	}
	if s.ExpiresAt, err = parseTime(expires); err != nil {
		return auth.Session{}, false, err
	}
	return s, true, nil
}

// DeleteSession implements auth.SessionStore.
func (db *DB) DeleteSession(ctx context.Context, hash string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hash)
	return err
}

// DeleteAllSessions implements auth.SessionStore.
func (db *DB) DeleteAllSessions(ctx context.Context) (int, error) {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM sessions`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteExpired implements auth.SessionStore.
func (db *DB) DeleteExpired(ctx context.Context, now time.Time) error {
	at := formatTime(now)
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM login_links WHERE expires_at <= ?`, at); err != nil {
		return err
	}
	_, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, at)
	return err
}
