package review

import (
	"context"
	"encoding/json"
	"time"
)

// Review statuses.
const (
	Pending = "pending" // published, nobody has taken it
	Claimed = "claimed" // a reviewer took it and holds the token
	Done    = "done"    // a verdict was posted
	Expired = "expired" // the step's timeout passed with no verdict
)

// Why the platform closed a review without a verdict (WU-068): the reason
// on review.expired and on the review, or on review.released for a claim
// given back.
const (
	ReasonTimedOut      = "timed_out"      // the step's timeout passed
	ReasonRunCancelled  = "run_cancelled"  // the run was cancelled: a newer iteration superseded it, or its change was closed
	ReasonServerStopped = "server_stopped" // the Plumbix server stopped while the run waited
	ReasonServerRestart = "server_restart" // the run was found running after a restart
	ReasonReviewerGone  = "reviewer_gone"  // the reviewer never received the brief; the review is pending again
	// Reasons a reviewer gives when it hands a claimed review back without
	// a verdict (WU-071): the step errors at once instead of waiting out
	// its timeout.
	ReasonCheckoutFailed = "checkout_failed" // the reviewer could not clone or check out the head
	ReasonReviewerError  = "reviewer_error"  // the reviewer ran but produced no verdict
)

// ReviewerReasons are the reasons POST /reviews/{id}/fail accepts.
var ReviewerReasons = []string{ReasonCheckoutFailed, ReasonReviewerError}

// MaxFailMessage bounds the message a reviewer sends with a failure.
const MaxFailMessage = 2000

// Credential is what the server fetches a repository with, handed to the
// reviewer holding a claim so its clone goes out with the same access
// (WU-071). It is never part of the brief and never stored with it.
type Credential struct {
	User  string `json:"user"`
	Token string `json:"token"`
}

// Review is one external validator step waiting for a verdict (WU-023).
// The token is minted when a reviewer claims the review, handed out once
// with the brief, and kept as a hash: only the claimer can post.
type Review struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	RepoID     string          `json:"repo_id"`
	ChangeID   string          `json:"change_id"`
	Step       string          `json:"step"`
	Status     string          `json:"status"`
	Reviewer   string          `json:"reviewer,omitempty"`
	TokenHash  string          `json:"-"`
	Brief      json.RawMessage `json:"-"` // the Brief as published, without the token
	Verdict    json.RawMessage `json:"-"` // the verdict as posted, once done
	CreatedAt  time.Time       `json:"created_at"`
	ClaimedAt  time.Time       `json:"claimed_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at"`
	// ExpiredReason says why the platform closed the review without a
	// verdict (WU-068), or why the reviewer gave it back (WU-071); empty
	// unless Status is Expired. ExpiredMessage is the reviewer's own words
	// for the latter, such as git's error.
	ExpiredReason  string `json:"expired_reason,omitempty"`
	ExpiredMessage string `json:"expired_message,omitempty"`
}

// The launcher marker (WU-069, contracts/launcher.md): what a Claude Code
// session Plumbix starts states about itself in its environment, so an
// observer's hook can tell who launched it and for what.
const (
	EnvLauncher   = "PLUMBIX_LAUNCHER"   // review (the loop), validator (the AI validator image)
	EnvRepository = "PLUMBIX_REPOSITORY" // the registration's url, byte for byte as GET /api/v1/repos returns it
	EnvRepoID     = "PLUMBIX_REPO_ID"
	EnvChange     = "PLUMBIX_CHANGE"
	EnvRun        = "PLUMBIX_RUN"
	EnvReview     = "PLUMBIX_REVIEW"

	LauncherReview    = "review"
	LauncherValidator = "validator"
)

// LauncherEnv is the environment a session started for this brief
// carries (WU-069): the launcher and the brief's repository url, ids of
// repository, change, run and review. A value the brief lacks leaves its
// variable absent, never empty, as the contract promises.
func (b Brief) LauncherEnv() []string {
	return LauncherVars(LauncherReview, map[string]string{
		EnvRepository: b.RepoURL, EnvRepoID: b.RepoID, EnvChange: b.ChangeID, EnvRun: b.RunID, EnvReview: b.ID,
	})
}

// LauncherVars builds the launcher marker's pairs: the launcher first,
// then the given variables in the contract's order, skipping empty values.
func LauncherVars(launcher string, values map[string]string) []string {
	env := []string{EnvLauncher + "=" + launcher}
	for _, name := range []string{EnvRepository, EnvRepoID, EnvChange, EnvRun, EnvReview} {
		if v := values[name]; v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// Brief is what a reviewer receives from GET /reviews/next: everything a
// container validator would get, in one document, plus the token that
// posts the verdict. Prompt and Schema are the AI validator's own, so a
// reviewer needs no knowledge of Plumbix's rules.
type Brief struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	ChangeID  string         `json:"change_id"`
	RepoID    string         `json:"repo_id"`
	RepoURL   string         `json:"repo_url"`
	Step      string         `json:"step"`
	Iteration int            `json:"iteration"`
	Head      string         `json:"head"`
	Base      string         `json:"base"`
	Token     string         `json:"token,omitempty"`
	ExpiresAt time.Time      `json:"expires_at"`
	Tools     string         `json:"tools"`              // read-only or full, from the reviewer profile
	Model     string         `json:"model,omitempty"`    // the profile's model, if set
	Reviewer  string         `json:"reviewer,omitempty"` // the reviewer profile's name, if one applies (WU-028)
	Prompt    Prompt         `json:"prompt"`
	Schema    map[string]any `json:"schema"`
	Criteria  []string       `json:"criteria"` // the criterion ids a verdict may name
}

// Tool policies for external reviewers and the container image.
const (
	ToolsReadOnly = "read-only"
	ToolsFull     = "full"
)

// Store persists reviews. The token is kept as a hash set at claim time.
type Store interface {
	CreateReview(ctx context.Context, r Review) error // emits review.requested
	// ClaimReview hands the oldest pending, unexpired review to reviewer,
	// never one of a change the reviewer submitted; ok is false when none waits.
	ClaimReview(ctx context.Context, repoID, reviewer, tokenHash string, at time.Time) (Review, bool, error)
	// PostReviewVerdict stores the verdict of a claimed review whose token
	// matches: ErrNotFound for an unknown review, ErrConflict otherwise.
	PostReviewVerdict(ctx context.Context, id, tokenHash string, verdict json.RawMessage, at time.Time) error
	ExpireReview(ctx context.Context, id string, at time.Time, reason string) error
	// FailReview closes a claimed review whose token matches with the
	// reviewer's reason and message (WU-071), emitting review.expired:
	// ErrNotFound for an unknown review, ErrConflict otherwise.
	FailReview(ctx context.Context, id, tokenHash string, at time.Time, reason, message string) error
	// ReleaseReview returns a claimed review to pending, for a claim whose
	// reviewer never received the brief (the connection dropped meanwhile).
	ReleaseReview(ctx context.Context, id string) error
	// ExpireRunReviews closes every open review of a run, for a run that
	// will never consume a verdict (the server restarted while it waited).
	ExpireRunReviews(ctx context.Context, runID string, at time.Time, reason string) ([]Review, error)
	GetReview(ctx context.Context, id string) (Review, error)
}
