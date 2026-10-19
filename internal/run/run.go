// Package run holds the record of one pipeline execution and the Store
// interface that persists it.
package run

import (
	"context"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Status is a run's lifecycle state.
type Status string

// Run statuses.
const (
	Running   Status = "running"
	Finished  Status = "finished"
	Errored   Status = "errored"
	Cancelled Status = "cancelled"
)

// Run is one pipeline execution for one commit. For a change, it is one
// iteration.
type Run struct {
	ID       string `json:"id"`
	RepoID   string `json:"repo_id"`
	ChangeID string `json:"change_id,omitempty"`
	Branch   string `json:"branch"`
	// Pipeline names the triggered pipeline the run ran (WU-045); empty for
	// the change pipeline, .plumbix/pipeline.yaml.
	Pipeline   string             `json:"pipeline,omitempty"`
	Iteration  int                `json:"iteration,omitempty"`
	Head       string             `json:"head"`
	Base       string             `json:"base"`
	Status     Status             `json:"status"`
	Outcome    change.State       `json:"outcome,omitempty"`
	Result     *validation.Result `json:"result,omitempty"`
	Error      string             `json:"error,omitempty"`
	Steps      []Step             `json:"steps"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
}

// Move is a branch head moving from Before to After; Before is empty for a
// new branch (WU-045).
type Move struct {
	Branch string
	Before string
	After  string
}

// Step is one step's outcome within a run.
type Step struct {
	Name      string `json:"name"`
	Validator bool   `json:"validator"`
	// Advisory marks a validator whose verdict was recorded but not counted (WU-030).
	Advisory   bool   `json:"advisory,omitempty"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	// Attempts is how many times the step ran; above one after retries.
	Attempts int `json:"attempts,omitempty"`
	// AllowedFailure marks a failure the step's allow_failure tolerated.
	AllowedFailure bool `json:"allowed_failure,omitempty"`
	// Reason says why a step was skipped: needs or paths.
	Reason string `json:"reason,omitempty"`
	// Stage is the pipeline stage the step belongs to, if any.
	Stage string `json:"stage,omitempty"`
}

// Finish is what a finished run records.
type Finish struct {
	Status  Status
	Outcome change.State
	Result  *validation.Result
	Error   string
	Steps   []Step
	At      time.Time
}

// Store persists runs.
type Store interface {
	CreateRun(ctx context.Context, r Run) error
	SetRunCommits(ctx context.Context, id, head, base string) error
	// RecordStep stores one step's outcome while the run is still going, at
	// its position in the pipeline, so clients see progress before the end.
	RecordStep(ctx context.Context, id string, position int, s Step) error
	FinishRun(ctx context.Context, id string, f Finish) error
	GetRun(ctx context.Context, id string) (Run, error) // change.ErrNotFound
	ListRuns(ctx context.Context, changeID string) ([]Run, error)
	LastIteration(ctx context.Context, changeID string) (int, error)
	RunningRuns(ctx context.Context) ([]Run, error)
	// ListRepoRuns returns a repo's runs, newest first, optionally for one
	// branch.
	ListRepoRuns(ctx context.Context, repoID, branch string, limit int) ([]Run, error)
}
