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
	RunFinished     Type = "run.finished"
	VerdictIssued   Type = "verdict.issued"
	AlertRaised     Type = "alert.raised"
	AlertResolved   Type = "alert.resolved"
	// ApprovedByPerson: a person approved a needs_human or escalated change.
	ApprovedByPerson Type = "change.approved_by_person"
)

// Types lists every event type.
var Types = []Type{ChangeSubmitted, StateChanged, ChangeMerged, RunStarted, StepFinished, RunFinished, VerdictIssued, AlertRaised, AlertResolved, ApprovedByPerson}

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
	}
	// StateChangedPayload: a change moved from one state to another.
	StateChangedPayload struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	// ChangeMergedPayload: Plumbix landed the change on its target branch.
	ChangeMergedPayload struct {
		Branch   string `json:"branch"`
		Commit   string `json:"commit"`
		Previous string `json:"previous"`
		Strategy string `json:"strategy"`
	}
	// ApprovedByPersonPayload: a person approved the change at Head, and why.
	ApprovedByPersonPayload struct {
		Reason string `json:"reason"`
		Head   string `json:"head"`
	}
	// RunStartedPayload: a pipeline run began; Iteration is set for a change's run.
	RunStartedPayload struct {
		Branch    string `json:"branch"`
		Iteration int    `json:"iteration,omitempty"`
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
