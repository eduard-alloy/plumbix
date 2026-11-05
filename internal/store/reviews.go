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

const reviewColumns = `id, run_id, repo_id, change_id, step, status, reviewer, token_hash, brief, verdict, created_at, claimed_at, finished_at, expires_at, expired_reason, expired_message`

// CreateReview publishes an external step's review and emits review.requested.
func (db *DB) CreateReview(ctx context.Context, r review.Review) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reviews (`+reviewColumns+`) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, '', ?, '', '', ?, '', '')`,
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
func (db *DB) ExpireReview(ctx context.Context, id string, at time.Time, reason string) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		r, err := scanReview(tx.QueryRowContext(ctx,
			`UPDATE reviews SET status = ?, finished_at = ?, expired_reason = ? WHERE id = ? AND status IN (?, ?) RETURNING `+reviewColumns,
			review.Expired, formatTime(at), reason, id, review.Pending, review.Claimed))
		if errors.Is(err, change.ErrNotFound) {
			// Already closed, or unknown: only the latter is an error.
			_, err = scanReview(tx.QueryRowContext(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE id = ?`, id))
			return err
		}
		if err != nil {
			return err
		}
		return emitExpired(emit, r, at, reason, "")
	})
}

// FailReview closes a claimed review on the reviewer's own word (WU-071):
// the token must match, as for a verdict, and the reason and message are
// written on the row and into review.expired in the same transaction.
func (db *DB) FailReview(ctx context.Context, id, tokenHash string, at time.Time, reason, message string) error {
	return db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		r, err := scanReview(tx.QueryRowContext(ctx,
			`UPDATE reviews SET status = ?, finished_at = ?, expired_reason = ?, expired_message = ?
			 WHERE id = ? AND token_hash = ? AND token_hash != '' AND status = ? RETURNING `+reviewColumns,
			review.Expired, formatTime(at), reason, message, id, tokenHash, review.Claimed))
		if errors.Is(err, change.ErrNotFound) {
			if _, err := scanReview(tx.QueryRowContext(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE id = ?`, id)); err != nil {
				return err
			}
			return change.ErrConflict
		}
		if err != nil {
			return err
		}
		return emitExpired(emit, r, at, reason, message)
	})
}

// emitExpired records review.expired for a review just closed without a
// verdict; the reviewer is named only when somebody held the claim, and
// the message only when the reviewer gave one.
func emitExpired(emit emitFunc, r review.Review, at time.Time, reason, message string) error {
	_, err := emit(events.Event{Type: events.ReviewExpired, Time: at, RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.RunID,
		Payload: events.ReviewExpiredPayload{ReviewID: r.ID, Reviewer: r.Reviewer, Reason: reason, Message: message}})
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
			Payload: events.ReviewReleasedPayload{ReviewID: r.ID, Reviewer: was, Reason: review.ReasonReviewerGone}})
		return err
	})
}

// ExpireRunReviews closes the open reviews of one run for the reason
// given (server_restart from crash recovery), one review.expired each,
// and returns the reviews it closed.
func (db *DB) ExpireRunReviews(ctx context.Context, runID string, at time.Time, reason string) ([]review.Review, error) {
	var closed []review.Review
	err := db.writeTx(ctx, func(tx *sql.Tx, emit emitFunc) error {
		closed = nil
		rows, err := tx.QueryContext(ctx,
			`UPDATE reviews SET status = ?, finished_at = ?, expired_reason = ? WHERE run_id = ? AND status IN (?, ?) RETURNING `+reviewColumns,
			review.Expired, formatTime(at), reason, runID, review.Pending, review.Claimed)
		if err != nil {
			return mapErr(err)
		}
		for rows.Next() {
			r, err := scanReview(rows)
			if err != nil {
				rows.Close()
				return err
			}
			closed = append(closed, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		for _, r := range closed {
			if err := emitExpired(emit, r, at, reason, ""); err != nil {
				return err
			}
		}
		return nil
	})
	return closed, err
}

// GetReview returns one review. ErrNotFound when there is none.
func (db *DB) GetReview(ctx context.Context, id string) (review.Review, error) {
	return scanReview(db.sql.QueryRowContext(ctx, `SELECT `+reviewColumns+` FROM reviews WHERE id = ?`, id))
}

func scanReview(s scanner) (review.Review, error) {
	var r review.Review
	var brief, verdict, created, claimed, finished, expires string
	if err := s.Scan(&r.ID, &r.RunID, &r.RepoID, &r.ChangeID, &r.Step, &r.Status, &r.Reviewer, &r.TokenHash, &brief, &verdict, &created, &claimed, &finished, &expires, &r.ExpiredReason, &r.ExpiredMessage); err != nil {
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
