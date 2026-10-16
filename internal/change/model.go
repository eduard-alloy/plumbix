// Package change holds the change and repo model, the rules for submitting
// them, and the service that applies those rules on top of a Store.
package change

import (
	"context"
	"errors"
	"time"
)

// State is a change's lifecycle state (ARCHITECTURE.md section 3).
type State string

// Change states.
const (
	Open         State = "open"
	Running      State = "running"
	Approved     State = "approved"
	FixRequested State = "fix_requested"
	Rejected     State = "rejected"
	NeedsHuman   State = "needs_human"
	Escalated    State = "escalated"
	Merged       State = "merged"
	Closed       State = "closed"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case Open, Running, Approved, FixRequested, Rejected, NeedsHuman, Escalated, Merged, Closed:
		return true
	}
	return false
}

// Final reports whether no further transitions are possible.
func (s State) Final() bool { return s == Merged || s == Closed }

// Repo is a registered git repository.
type Repo struct {
	ID                string    `json:"id"`
	Host              string    `json:"host"`
	URL               string    `json:"url"`
	ProtectedBranches []string  `json:"protected_branches"`
	MergeStrategy     string    `json:"merge_strategy"`
	CreatedAt         time.Time `json:"created_at"`
}

// Change is a proposed change with its intent and acceptance criteria.
type Change struct {
	ID                 string      `json:"id"`
	RepoID             string      `json:"repo_id"`
	SourceBranch       string      `json:"source_branch"`
	TargetBranch       string      `json:"target_branch"`
	Title              string      `json:"title"`
	Intent             string      `json:"intent"`
	AcceptanceCriteria []Criterion `json:"acceptance_criteria"`
	Constraints        []string    `json:"constraints"`
	SubmittedBy        string      `json:"submitted_by,omitempty"`
	State              State       `json:"state"`
	Merge              *Merge      `json:"merge,omitempty"` // set once merged
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
}

// Merge says how a change landed on its target branch.
type Merge struct {
	Commit   string    `json:"commit"`   // the commit now at the target's head
	Previous string    `json:"previous"` // the target's head before the merge
	Strategy string    `json:"strategy"`
	RunID    string    `json:"run_id"` // the approved run that was merged
	MergedAt time.Time `json:"merged_at"`
}

// MergeRecord is a Merge with the change and branch it belongs to.
type MergeRecord struct {
	ChangeID string
	RepoID   string
	Branch   string
	Merge
}

// Criterion is one acceptance criterion.
type Criterion struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Check *Check `json:"check,omitempty"`
}

// Check says how the deterministic validator proves a criterion: either
// pipeline steps that must succeed, or a command to run.
type Check struct {
	Steps   []string `json:"steps,omitempty"`
	Command string   `json:"command,omitempty"`
}

// Filter narrows a change listing. Empty fields match everything.
type Filter struct {
	RepoID string
	State  State
}

// Errors returned by a Store and the Service.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Store persists repos and changes.
type Store interface {
	CreateRepo(ctx context.Context, r Repo) error         // ErrConflict on a duplicate URL
	GetRepo(ctx context.Context, id string) (Repo, error) // ErrNotFound
	ListRepos(ctx context.Context) ([]Repo, error)
	CreateChange(ctx context.Context, c Change) error         // ErrConflict if another change is open on the branch
	GetChange(ctx context.Context, id string) (Change, error) // ErrNotFound
	ListChanges(ctx context.Context, f Filter) ([]Change, error)
	// SetChangeState moves a change from one state to another. It returns
	// ErrConflict if the change is no longer in state from.
	SetChangeState(ctx context.Context, id string, from, to State, at time.Time) error
	RecordMerge(ctx context.Context, m MergeRecord) error // ErrConflict if the change was merged already
	// ListMerges returns the merges Plumbix pushed to a branch, oldest first.
	ListMerges(ctx context.Context, repoID, branch string) ([]MergeRecord, error)
}
