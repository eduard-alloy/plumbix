// Package store persists Plumbix data in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/eduard-alloy/plumbix/internal/change"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// busyTimeoutMS is how long a connection waits for a lock before failing.
const busyTimeoutMS = 5000

// DB is a SQLite-backed change.Store.
type DB struct {
	sql *sql.DB
}

var _ change.Store = (*DB)(nil)

// Open opens or creates the database at path and applies pending migrations.
func Open(path string) (*DB, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	conn, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db := &DB{sql: conn}
	if err := db.migrate(context.Background()); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.sql.Close() }

type migration struct {
	version int
	name    string
}

func migrations() ([]migration, error) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, n := range names {
		base := strings.TrimPrefix(n, "migrations/")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number", base)
		}
		out = append(out, migration{version: v, name: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func latestVersion() int {
	ms, _ := migrations()
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	current, err := db.schemaVersion(ctx)
	if err != nil {
		return err
	}
	ms, err := migrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		body, err := migrationFiles.ReadFile(m.name)
		if err != nil {
			return err
		}
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, formatTime(time.Now())); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", m.name, err)
		}
	}
	return nil
}

func (db *DB) schemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}

// CreateRepo stores a new repo.
func (db *DB) CreateRepo(ctx context.Context, r change.Repo) error {
	branches, err := json.Marshal(r.ProtectedBranches)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO repos (id, host, url, protected_branches, merge_strategy, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Host, r.URL, string(branches), r.MergeStrategy, formatTime(r.CreatedAt))
	return mapErr(err)
}

const repoColumns = `id, host, url, protected_branches, merge_strategy, created_at`

// GetRepo returns one repo.
func (db *DB) GetRepo(ctx context.Context, id string) (change.Repo, error) {
	return scanRepo(db.sql.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM repos WHERE id = ?`, id))
}

// ListRepos returns every repo, oldest first.
func (db *DB) ListRepos(ctx context.Context) ([]change.Repo, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+repoColumns+` FROM repos ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repos := []change.Repo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanRepo(s scanner) (change.Repo, error) {
	var r change.Repo
	var branches, created string
	if err := s.Scan(&r.ID, &r.Host, &r.URL, &branches, &r.MergeStrategy, &created); err != nil {
		return change.Repo{}, mapErr(err)
	}
	if err := json.Unmarshal([]byte(branches), &r.ProtectedBranches); err != nil {
		return change.Repo{}, fmt.Errorf("repo %s: protected branches: %w", r.ID, err)
	}
	var err error
	r.CreatedAt, err = parseTime(created)
	return r, err
}

// CreateChange stores a new change.
func (db *DB) CreateChange(ctx context.Context, c change.Change) error {
	criteria, err := json.Marshal(c.AcceptanceCriteria)
	if err != nil {
		return err
	}
	constraints, err := json.Marshal(nonNil(c.Constraints))
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO changes (id, repo_id, source_branch, target_branch, title, intent, acceptance_criteria, constraints, submitted_by, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.RepoID, c.SourceBranch, c.TargetBranch, c.Title, c.Intent, string(criteria), string(constraints),
		c.SubmittedBy, string(c.State), formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	return mapErr(err)
}

const changeColumns = `id, repo_id, source_branch, target_branch, title, intent, acceptance_criteria, constraints, submitted_by, state, created_at, updated_at`

// GetChange returns one change.
func (db *DB) GetChange(ctx context.Context, id string) (change.Change, error) {
	return scanChange(db.sql.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM changes WHERE id = ?`, id))
}

// ListChanges returns matching changes, oldest first.
func (db *DB) ListChanges(ctx context.Context, f change.Filter) ([]change.Change, error) {
	query := `SELECT ` + changeColumns + ` FROM changes WHERE 1 = 1`
	var args []any
	if f.RepoID != "" {
		query += ` AND repo_id = ?`
		args = append(args, f.RepoID)
	}
	if f.State != "" {
		query += ` AND state = ?`
		args = append(args, string(f.State))
	}
	rows, err := db.sql.QueryContext(ctx, query+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []change.Change{}
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// SetChangeState moves a change from one state to another.
func (db *DB) SetChangeState(ctx context.Context, id string, from, to change.State, at time.Time) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE changes SET state = ?, updated_at = ? WHERE id = ? AND state = ?`,
		string(to), formatTime(at), id, string(from))
	if err != nil {
		return mapErr(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	if _, err := db.GetChange(ctx, id); err != nil {
		return err
	}
	return change.ErrConflict
}

func scanChange(s scanner) (change.Change, error) {
	var c change.Change
	var criteria, constraints, state, created, updated string
	if err := s.Scan(&c.ID, &c.RepoID, &c.SourceBranch, &c.TargetBranch, &c.Title, &c.Intent,
		&criteria, &constraints, &c.SubmittedBy, &state, &created, &updated); err != nil {
		return change.Change{}, mapErr(err)
	}
	c.State = change.State(state)
	if err := json.Unmarshal([]byte(criteria), &c.AcceptanceCriteria); err != nil {
		return change.Change{}, fmt.Errorf("change %s: criteria: %w", c.ID, err)
	}
	if err := json.Unmarshal([]byte(constraints), &c.Constraints); err != nil {
		return change.Change{}, fmt.Errorf("change %s: constraints: %w", c.ID, err)
	}
	var err error
	if c.CreatedAt, err = parseTime(created); err != nil {
		return change.Change{}, err
	}
	c.UpdatedAt, err = parseTime(updated)
	return c, err
}

// mapErr translates database errors into change package errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return change.ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) && (se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return fmt.Errorf("%w: %v", change.ErrConflict, err)
	}
	return err
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", s, err)
	}
	return t, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
