// Package events defines what Plumbix announces: one event per fact, with
// an increasing id so clients can resume where they stopped. The store
// appends events in the same transaction as the data they describe; the
// Hub fans them out to live subscribers.
package events

import (
	"context"
	"time"
)

// Type names what happened.
type Type string

// Event types. The list in Types and contracts/events.schema.json must
// stay the same set; a contract test checks it.
const (
	ChangeSubmitted Type = "change.submitted"
	StateChanged    Type = "change.state_changed"
	ChangeMerged    Type = "change.merged"
	RunStarted      Type = "run.started"
	StepFinished    Type = "step.finished"
	// StepStarted: a step began; the run lists it as running from now on.
	StepStarted   Type = "step.started"
	RunFinished   Type = "run.finished"
	VerdictIssued Type = "verdict.issued"
	AlertRaised   Type = "alert.raised"
	AlertResolved Type = "alert.resolved"
	// ApprovedByPerson: a person approved a needs_human or escalated change.
	ApprovedByPerson Type = "change.approved_by_person"
	// ChangeRefreshed: Plumbix merged the target branch into a branch it
	// created and pushed the result; a new run follows.
	ChangeRefreshed Type = "change.refreshed"
	// ReviewRequested: an external validator step is waiting for a verdict
	// from outside the pipeline (WU-023).
	ReviewRequested Type = "review.requested"
	// ReviewClaimed: a reviewer took the review and holds its token.
	ReviewClaimed Type = "review.claimed"
	// ReviewReleased: a claim was given back because the reviewer never
	// received the brief; the review is pending again.
	ReviewReleased Type = "review.released"
)

// Types lists every event type.
var Types = []Type{ChangeSubmitted, StateChanged, ChangeMerged, RunStarted, StepFinished, RunFinished, VerdictIssued, AlertRaised, AlertResolved, ApprovedByPerson, ChangeRefreshed, ReviewRequested, ReviewClaimed, ReviewReleased, StepStarted}

// Event is the envelope every event shares. The three optional ids are the
// filter keys; Payload holds what the type needs and is one of the payload
// structs below (or a decoded JSON value when read back from the store).
type Event struct {
	ID       int64     `json:"id"`
	Type     Type      `json:"type"`
	Time     time.Time `json:"time"`
	RepoID   string    `json:"repo_id,omitempty"`
	ChangeID string    `json:"change_id,omitempty"`
	RunID    string    `json:"run_id,omitempty"`
	Payload  any       `json:"payload"`
}

// Payloads, one per type.
type (
	// ChangeSubmittedPayload: a change was created, state open.
	ChangeSubmittedPayload struct {
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Title        string `json:"title"`
		SubmittedBy  string `json:"submitted_by,omitempty"`
		// PullRequest is the GitHub pull request the change mirrors (WU-054).
		PullRequest *PullRequestPayload `json:"pull_request,omitempty"`
	}
	// PullRequestPayload names a pull request by number and URL.
	PullRequestPayload struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	// StateChangedPayload: a change moved from one state to another.
	StateChangedPayload struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	// ChangeMergedPayload: the change landed on its target branch, pushed by
	// Plumbix or, with strategy github, merged by GitHub (WU-054).
	ChangeMergedPayload struct {
		Branch   string `json:"branch"`
		Commit   string `json:"commit"`
		Previous string `json:"previous"`
		Strategy string `json:"strategy"`
		MergedBy string `json:"merged_by,omitempty"`
	}
	// ApprovedByPersonPayload: a person approved the change at Head, and why.
	ApprovedByPersonPayload struct {
		Reason string `json:"reason"`
		Head   string `json:"head"`
	}
	// ChangeRefreshedPayload: the source branch moved from FromHead to Head,
	// a merge commit that brings in the target's TargetHead.
	ChangeRefreshedPayload struct {
		FromHead   string `json:"from_head"`
		Head       string `json:"head"`
		TargetHead string `json:"target_head"`
	}
	// ReviewRequestedPayload: the step Step of the run waits for a verdict
	// until ExpiresAt; GET /reviews/next hands out the brief.
	ReviewRequestedPayload struct {
		ReviewID  string    `json:"review_id"`
		Step      string    `json:"step"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	// ReviewClaimedPayload: Reviewer took the review.
	ReviewClaimedPayload struct {
		ReviewID string `json:"review_id"`
		Reviewer string `json:"reviewer"`
	}
	// ReviewReleasedPayload: Reviewer's claim was given back.
	ReviewReleasedPayload struct {
		ReviewID string `json:"review_id"`
		Reviewer string `json:"reviewer"`
	}
	// RunStartedPayload: a pipeline run began; Iteration is set for a change's run.
	RunStartedPayload struct {
		Branch    string `json:"branch"`
		Iteration int    `json:"iteration,omitempty"`
		// Pipeline names a triggered pipeline (WU-045).
		Pipeline string `json:"pipeline,omitempty"`
	}
	// StepStartedPayload: one step of a run began.
	StepStartedPayload struct {
		Step      string `json:"step"`
		Validator bool   `json:"validator"`
		Stage     string `json:"stage,omitempty"`
	}
	// StepFinishedPayload: one step of a run ended.
	StepFinishedPayload struct {
		Step       string `json:"step"`
		Validator  bool   `json:"validator"`
		Status     string `json:"status"`
		ExitCode   int    `json:"exit_code"`
		DurationMS int64  `json:"duration_ms"`
		Error      string `json:"error,omitempty"`
		// Attempts is above one when the step was retried.
		Attempts int `json:"attempts,omitempty"`
		// AllowedFailure marks a failure the step's allow_failure tolerated.
		AllowedFailure bool `json:"allowed_failure,omitempty"`
		// Reason says why a step was skipped: needs or paths.
		Reason string `json:"reason,omitempty"`
	}
	// RunFinishedPayload: a run ended, with its outcome for a change's run.
	RunFinishedPayload struct {
		Branch    string `json:"branch"`
		Iteration int    `json:"iteration,omitempty"`
		Status    string `json:"status"`
		Outcome   string `json:"outcome,omitempty"`
		Error     string `json:"error,omitempty"`
		Head      string `json:"head"`
		Base      string `json:"base"`
	}
	// VerdictIssuedPayload: the validators' combined judgment of an iteration.
	VerdictIssuedPayload struct {
		Iteration int               `json:"iteration"`
		Outcome   string            `json:"outcome"`
		Criteria  []CriterionStatus `json:"criteria"`
		Findings  int               `json:"findings"`
		Reasons   []string          `json:"reasons"`
	}
	// CriterionStatus is one criterion's combined status.
	CriterionStatus struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	// AlertRaisedPayload: a new alert is open.
	AlertRaisedPayload struct {
		AlertID string `json:"alert_id"`
		Kind    string `json:"kind"`
		Key     string `json:"key"`
		Message string `json:"message"`
	}
	// AlertResolvedPayload: an alert was closed.
	AlertResolvedPayload struct {
		AlertID string `json:"alert_id"`
		Kind    string `json:"kind"`
		Key     string `json:"key"`
	}
)

// Filter narrows a stream. Empty fields match everything.
type Filter struct {
	RepoID   string
	ChangeID string
}

// Matches reports whether e passes the filter.
func (f Filter) Matches(e Event) bool {
	return (f.RepoID == "" || e.RepoID == f.RepoID) && (f.ChangeID == "" || e.ChangeID == f.ChangeID)
}

// Reader reads recorded events.
type Reader interface {
	// EventsAfter returns up to limit events with an id greater than after,
	// oldest first, matching f.
	EventsAfter(ctx context.Context, after int64, f Filter, limit int) ([]Event, error)
}

// Appender records an event that no other write produces.
type Appender interface {
	// AppendEvent stores e, assigning its id (and its time when zero), and
	// returns it as stored.
	AppendEvent(ctx context.Context, e Event) (Event, error)
}

// Store reads and appends events.
type Store interface {
	Reader
	Appender
}
