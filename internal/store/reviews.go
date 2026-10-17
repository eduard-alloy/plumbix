package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/review"
)

const reviewColumns = `id, run_id, repo_id, change_id, step, status, reviewer, token_hash, brief, verdict, created_at, claimed_at, finished_at, expires_at`

// CreateReview publishes an external step's review and emits review.requested.
func (db *DB) CreateReview(ctx context.Context, r review.Review) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reviews (`+reviewColumns+`) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, '', ?, '', '', ?)`,
			r.ID, r.RunID, r.RepoID, r.ChangeID, r.Step, review.Pending, string(r.Brief), formatTime(r.CreatedAt), formatTime(r.ExpiresAt)); err != nil {
			return mapErr(err)
		}
		_, err := emit(events.Event{Type: events.ReviewRequested, Time: r.CreatedAt, RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.RunID,
			Payload: events.ReviewRequestedPayload{ReviewID: r.ID, Step: r.Step, ExpiresAt: r.ExpiresAt}})
		return err
	})
}

// ClaimReview hands the oldest pending, unexpired review to reviewer,
// records the hash of the token the reviewer received, and emits
// review.claimed. repoID narrows to one repository when set. A reviewer
// never gets a change it submitted itself. ok is false when nothing is
// waiting.
func (db *DB) ClaimReview(ctx context.Context, repoID, reviewer, tokenHash string, at time.Time) (review.Review, bool, error) {
	var r review.Review
	found := false
	err := db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		row := tx.QueryRowContext(ctx,
			`UPDATE reviews SET status = ?, reviewer = ?, token_hash = ?, claimed_at = ?
			 WHERE id = (SELECT r.id FROM reviews r JOIN changes c ON c.id = r.change_id
			             WHERE r.status = ? AND r.expires_at > ? AND (? = '' OR r.repo_id = ?) AND c.submitted_by != ?
			             ORDER BY r.created_at, r.id LIMIT 1)
			 RETURNING `+reviewColumns,
			review.Claimed, reviewer, tokenHash, formatTime(at), review.Pending, formatTime(at), repoID, repoID, reviewer)
		var err error
		if r, err = scanReview(row); err != nil {
			if errors.Is(err, change.ErrNotFound) {
				return nil
			}
			return err
		}
		found = true
		_, err = emit(events.Event{Type: events.ReviewClaimed, Time: at, RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.RunID,
			Payload: events.ReviewClaimedPayload{ReviewID: r.ID, Reviewer: reviewer}})
		return err
	})
	return r, found, err
}

// PostReviewVerdict stores the verdict of a claimed, unexpired review whose
// token hash matches, and marks it done. ErrNotFound for an unknown review;
// ErrConflict for a wrong token, a review not claimed, already answered or
// expired. The step's outcome follows as step.finished, so no event here.
func (db *DB) PostReviewVerdict(ctx context.Context, id, tokenHash string, verdict json.RawMessage, at time.Time) error {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE reviews SET status = ?, verdict = ?, finished_at = ?
		 WHERE id = ? AND token_hash = ? AND token_hash != '' AND status = ? AND expires_at > ?`,
		review.Done, string(verdict), formatTime(at), id, tokenHash, review.Claimed, formatTime(at))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := db.GetReview(ctx, id); err != nil {
		return err
	}
	return change.ErrConflict
}

// ExpireReview closes a review that got no verdict in time. A review that
// is already done or expired is left as it is.
func (db *DB) ExpireReview(ctx context.Context, id string, at time.Time) error {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE reviews SET status = ?, finished_at = ? WHERE id = ? AND status IN (?, ?)`,
		review.Expired, formatTime(at), id, review.Pending, review.Claimed)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	_, err = db.GetReview(ctx, id)
	return err
}

// ReleaseReview returns a claimed review to pending with no reviewer and
// no token, so the next claim can take it, and emits review.released. A
// review that is not claimed is left as it is.
func (db *DB) ReleaseReview(ctx context.Context, id string) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		var r review.Review
		var was string
		if err := tx.QueryRowContext(ctx, `SELECT reviewer FROM reviews WHERE id = ? AND status = ?`, id, review.Claimed).Scan(&was); err != nil {
			if !errors.Is(mapErr(err), change.ErrNotFound) {
				return mapErr(err)
			}
			// Unknown, or simply not claimed: only the former is an error.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reviews WHERE id = ?`, id).Scan(&n); err != nil {
				return mapErr(err)
			}
			if n == 0 {
				return change.ErrNotFound
			}
			return nil
		}
		var err error
		if r, err = scanReview(tx.QueryRowContext(ctx,
			`UPDATE reviews SET status = ?, reviewer = '', token_hash = '', claimed_at = '' WHERE id = ? AND status = ? RETURNING `+reviewColumns,
			review.Pending, id, review.Claimed)); err != nil {
			return err
		}
		_, err = emit(events.Event{Type: events.ReviewReleased, Time: time.Now().UTC(), RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.RunID,
			Payload: events.ReviewReleasedPayload{ReviewID: r.ID, Reviewer: was}})
		return err
	})
}

// ExpireRunReviews closes the open reviews of one run.
func (db *DB) ExpireRunReviews(ctx context.Context, runID string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE reviews SET status = ?, finished_at = ? WHERE run_id = ? AND status IN (?, ?)`,
		review.Expired, formatTime(at), runID, review.Pending, review.Claimed)
	return mapErr(err)
}

// GetReview returns one review. ErrNotFound when there is none.
func (db *DB) GetReview(ctx context.Context, id string) (review.Review, error) {
	return scanReview(db.sql.QueryRowContext(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE id = ?`, id))
}

func scanReview(s scanner) (review.Review, error) {
	var r review.Review
	var brief, verdict, created, claimed, finished, expires string
	if err := s.Scan(&r.ID, &r.RunID, &r.RepoID, &r.ChangeID, &r.Step, &r.Status, &r.Reviewer, &r.TokenHash, &brief, &verdict, &created, &claimed, &finished, &expires); err != nil {
		return review.Review{}, mapErr(err)
	}
	r.Brief = json.RawMessage(brief)
	if verdict != "" {
		r.Verdict = json.RawMessage(verdict)
	}
	var err error
	for _, f := range []struct {
		s  string
		at *time.Time
	}{{created, &r.CreatedAt}, {claimed, &r.ClaimedAt}, {finished, &r.FinishedAt}, {expires, &r.ExpiresAt}} {
		if f.s == "" {
			continue
		}
		if *f.at, err = parseTime(f.s); err != nil {
			return review.Review{}, err
		}
	}
	return r, nil
}
