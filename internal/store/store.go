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
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// busyTimeoutMS is how long a connection waits for a lock before failing.
const busyTimeoutMS = 5000

// DB is a SQLite-backed change.Store. It is also the event source: writes
// that change what a client sees append an event in the same transaction
// (see events.go).
type DB struct {
	sql    *sql.DB
	emitMu sync.Mutex         // one emitting write at a time, so events publish in id order
	notify func(events.Event) // called after commit for each event; nil discards
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
	merger := r.Merger
	if merger == "" {
		merger = change.MergerPlumbix
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO repos (id, host, url, protected_branches, merge_strategy, pull_requests, merger, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Host, r.URL, string(branches), r.MergeStrategy, r.PullRequests, merger, formatTime(r.CreatedAt))
	return mapErr(err)
}

const repoColumns = `id, host, url, protected_branches, merge_strategy, pull_requests, merger, created_at`

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
	if err := s.Scan(&r.ID, &r.Host, &r.URL, &branches, &r.MergeStrategy, &r.PullRequests, &r.Merger, &created); err != nil {
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
	var prNumber int
	var prURL string
	var prPayload *events.PullRequestPayload
	if c.PullRequest != nil {
		prNumber, prURL = c.PullRequest.Number, c.PullRequest.URL
		prPayload = &events.PullRequestPayload{Number: prNumber, URL: prURL}
	}
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO changes (id, repo_id, source_branch, target_branch, title, intent, acceptance_criteria, constraints, submitted_by, state, created_at, updated_at, plumbix_branch, pr_number, pr_url)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.RepoID, c.SourceBranch, c.TargetBranch, c.Title, c.Intent, string(criteria), string(constraints),
			c.SubmittedBy, string(c.State), formatTime(c.CreatedAt), formatTime(c.UpdatedAt), c.PlumbixBranch, prNumber, prURL); err != nil {
			return mapErr(err)
		}
		_, err := emit(events.Event{Type: events.ChangeSubmitted, Time: c.CreatedAt, RepoID: c.RepoID, ChangeID: c.ID,
			Payload: events.ChangeSubmittedPayload{SourceBranch: c.SourceBranch, TargetBranch: c.TargetBranch, Title: c.Title, SubmittedBy: c.SubmittedBy, PullRequest: prPayload}})
		return err
	})
}

const changeColumns = `c.id, c.repo_id, c.source_branch, c.target_branch, c.title, c.intent, c.acceptance_criteria, c.constraints, c.submitted_by, c.state, c.created_at, c.updated_at,
	m.commit_sha, m.previous_sha, m.strategy, m.run_id, m.merged_by, COALESCE(m.auto_merge, 0), m.merged_at, COALESCE(b.head, ''), c.approved_reason, c.approved_head, c.approved_at, c.approved_by, c.approved_source, c.plumbix_branch, c.pr_number, c.pr_url`

// changeFrom joins each change with its merge, if any, and the last head
// the poller saw on its source branch.
const changeFrom = ` FROM changes c LEFT JOIN merges m ON m.change_id = c.id LEFT JOIN branch_heads b ON b.repo_id = c.repo_id AND b.branch = c.source_branch`

// GetChange returns one change.
func (db *DB) GetChange(ctx context.Context, id string) (change.Change, error) {
	return scanChange(db.sql.QueryRowContext(ctx, `SELECT `+changeColumns+changeFrom+` WHERE c.id = ?`, id))
}

// ChangeByPullRequest returns the open (not merged or closed) change that
// mirrors a repository's pull request; change.ErrNotFound when none does.
func (db *DB) ChangeByPullRequest(ctx context.Context, repoID string, number int) (change.Change, error) {
	return scanChange(db.sql.QueryRowContext(ctx, `SELECT `+changeColumns+changeFrom+` WHERE c.repo_id = ? AND c.pr_number = ? AND c.state NOT IN ('merged', 'closed') ORDER BY c.created_at DESC LIMIT 1`, repoID, number))
}

// ListChanges returns matching changes, oldest first.
func (db *DB) ListChanges(ctx context.Context, f change.Filter) ([]change.Change, error) {
	query := `SELECT ` + changeColumns + changeFrom + ` WHERE 1 = 1`
	var args []any
	if f.RepoID != "" {
		query += ` AND c.repo_id = ?`
		args = append(args, f.RepoID)
	}
	if f.State != "" {
		query += ` AND c.state = ?`
		args = append(args, string(f.State))
	}
	rows, err := db.sql.QueryContext(ctx, query+` ORDER BY c.created_at, c.id`, args...)
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
	err := db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var repoID string
		// A new run judges a new head, so a person's approval ends with it.
		clear := ""
		if to == change.Running {
			clear = ", approved_reason = '', approved_head = '', approved_at = '', approved_by = '', approved_source = ''"
		}
		if err := tx.QueryRowContext(ctx, `UPDATE changes SET state = ?, updated_at = ?`+clear+` WHERE id = ? AND state = ? RETURNING repo_id`,
			string(to), formatTime(at), id, string(from)).Scan(&repoID); err != nil {
			return mapErr(err) // ErrNotFound: no row in state from
		}
		_, err := emit(events.Event{Type: events.StateChanged, Time: at, RepoID: repoID, ChangeID: id,
			Payload: events.StateChangedPayload{From: string(from), To: string(to)}})
		return err
	})
	if !errors.Is(err, change.ErrNotFound) {
		return err
	}
	if _, err := db.GetChange(ctx, id); err != nil {
		return err
	}
	return change.ErrConflict
}

// UpdateChangeText rewrites a change's title, intent and criteria (WU-062).
// A final change is ErrInvalidState; no event is emitted, since the next
// run carries the text it judged.
func (db *DB) UpdateChangeText(ctx context.Context, id, title, intent string, criteria []change.Criterion, at time.Time) error {
	if criteria == nil {
		criteria = []change.Criterion{}
	}
	data, err := json.Marshal(criteria)
	if err != nil {
		return err
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE changes SET title = ?, intent = ?, acceptance_criteria = ?, updated_at = ? WHERE id = ? AND state NOT IN (?, ?)`,
		title, intent, string(data), formatTime(at), id, string(change.Merged), string(change.Closed))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	c, err := db.GetChange(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: change %s is %s", change.ErrInvalidState, id, c.State)
}

// ApproveByPerson records a person's approval of a change at head and moves
// it from its current state to approved, emitting the approval and the
// state change together. ErrConflict when the change left state from.
func (db *DB) ApproveByPerson(ctx context.Context, id string, from change.State, ap change.HumanApproval) error {
	at := ap.At
	err := db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var repoID string
		if err := tx.QueryRowContext(ctx,
			`UPDATE changes SET state = ?, updated_at = ?, approved_reason = ?, approved_head = ?, approved_at = ?, approved_by = ?, approved_source = ? WHERE id = ? AND state = ? RETURNING repo_id`,
			string(change.Approved), formatTime(at), ap.Reason, ap.Head, formatTime(at), ap.By, ap.Source, id, string(from)).Scan(&repoID); err != nil {
			return mapErr(err)
		}
		if _, err := emit(events.Event{Type: events.ApprovedByPerson, Time: at, RepoID: repoID, ChangeID: id,
			Payload: events.ApprovedByPersonPayload{Reason: ap.Reason, Head: ap.Head, By: ap.By, Source: ap.Source}}); err != nil {
			return err
		}
		_, err := emit(events.Event{Type: events.StateChanged, Time: at, RepoID: repoID, ChangeID: id,
			Payload: events.StateChangedPayload{From: string(from), To: string(change.Approved)}})
		return err
	})
	if !errors.Is(err, change.ErrNotFound) {
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
	var mCommit, mPrevious, mStrategy, mRun, mBy, mAt sql.NullString
	var mAuto sql.NullBool
	var apReason, apHead, apAt, apBy, apSource string
	var prNumber int
	var prURL string
	if err := s.Scan(&c.ID, &c.RepoID, &c.SourceBranch, &c.TargetBranch, &c.Title, &c.Intent,
		&criteria, &constraints, &c.SubmittedBy, &state, &created, &updated,
		&mCommit, &mPrevious, &mStrategy, &mRun, &mBy, &mAuto, &mAt, &c.SourceHead, &apReason, &apHead, &apAt, &apBy, &apSource, &c.PlumbixBranch, &prNumber, &prURL); err != nil {
		return change.Change{}, mapErr(err)
	}
	c.State = change.State(state)
	if prNumber > 0 {
		c.PullRequest = &change.PullRequestRef{Number: prNumber, URL: prURL}
	}
	if apAt != "" {
		at, err := parseTime(apAt)
		if err != nil {
			return change.Change{}, err
		}
		c.HumanApproval = &change.HumanApproval{Reason: apReason, Head: apHead, At: at, By: apBy, Source: apSource}
	}
	if mCommit.Valid {
		at, err := parseTime(mAt.String)
		if err != nil {
			return change.Change{}, err
		}
		c.Merge = &change.Merge{Commit: mCommit.String, Previous: mPrevious.String, Strategy: mStrategy.String, RunID: mRun.String, MergedBy: mBy.String, AutoMerge: mAuto.Bool, MergedAt: at}
	}
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

// UpdateRepoMerging sets whether Plumbix watches a repository's pull
// requests and who merges them (WU-054).
func (db *DB) UpdateRepoMerging(ctx context.Context, id string, pullRequests bool, merger string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE repos SET pull_requests = ?, merger = ? WHERE id = ?`, pullRequests, merger, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return change.ErrNotFound
	}
	return nil
}
