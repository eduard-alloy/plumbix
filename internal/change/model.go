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

// NeedsPerson reports whether only a person may move the change on: a push
// to its branch starts nothing (A21).
func (s State) NeedsPerson() bool { return s == NeedsHuman || s == Escalated || s == Rejected }

// Who lands a change on its target branch (WU-054).
const (
	MergerPlumbix = "plumbix" // Plumbix pushes the validated tree
	MergerGitHub  = "github"  // GitHub merges the pull request; Plumbix records it
)

// StrategyGitHub is the merge strategy recorded when GitHub merged a pull
// request: GitHub does not say which of its methods it used.
const StrategyGitHub = "github"

// Repo is a registered git repository.
type Repo struct {
	ID                string   `json:"id"`
	Host              string   `json:"host"`
	URL               string   `json:"url"`
	ProtectedBranches []string `json:"protected_branches"`
	MergeStrategy     string   `json:"merge_strategy"`
	// PullRequests says Plumbix watches the repository's pull requests and
	// gates them with check runs (WU-054); GitHub only.
	PullRequests bool `json:"pull_requests,omitempty"`
	// Merger is who lands a change: MergerPlumbix or MergerGitHub. GitHub
	// merges only pull requests; a change without one still merges through
	// Plumbix.
	Merger    string    `json:"merger"`
	CreatedAt time.Time `json:"created_at"`
}

// PullRequestRef ties a change to the pull request it mirrors (WU-054).
type PullRequestRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
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
	// PullRequest is set for a change that mirrors a GitHub pull request.
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
	State       State           `json:"state"`
	Merge       *Merge          `json:"merge,omitempty"`       // set once merged
	SourceHead  string          `json:"source_head,omitempty"` // the source branch's head as the poller last saw it
	// HumanApproval is set when a person approved the change at Head; a
	// new run clears it.
	HumanApproval *HumanApproval `json:"human_approval,omitempty"`
	// PlumbixBranch says Plumbix created the source branch (the pipeline
	// editor's changes), so Plumbix may bring the target into it (WU-022).
	PlumbixBranch bool      `json:"plumbix_branch,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// HumanApproval records that a person approved a change, and why.
type HumanApproval struct {
	Reason string    `json:"reason"`
	Head   string    `json:"head"` // the validated commit the person approved
	At     time.Time `json:"at"`
}

// Merge says how a change landed on its target branch.
type Merge struct {
	Commit   string `json:"commit"`   // the commit now at the target's head
	Previous string `json:"previous"` // the target's head before the merge
	Strategy string `json:"strategy"`
	RunID    string `json:"run_id"` // the approved run that was merged; empty when GitHub merged before any run
	// MergedBy is the GitHub login that merged a pull request; empty for
	// Plumbix's own merges.
	MergedBy string    `json:"merged_by,omitempty"`
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
	// UpdateRepoMerging sets a repository's pull_requests and merger
	// (WU-055). ErrNotFound for an unknown repository.
	UpdateRepoMerging(ctx context.Context, id string, pullRequests bool, merger string) error
	CreateChange(ctx context.Context, c Change) error         // ErrConflict if another change is open on the branch
	GetChange(ctx context.Context, id string) (Change, error) // ErrNotFound
	ListChanges(ctx context.Context, f Filter) ([]Change, error)
	// SetChangeState moves a change from one state to another. It returns
	// ErrConflict if the change is no longer in state from.
	SetChangeState(ctx context.Context, id string, from, to State, at time.Time) error
	// ApproveByPerson records a person's approval at head and moves the
	// change from state from to approved. ErrConflict if it left state from.
	ApproveByPerson(ctx context.Context, id string, from State, reason, head string, at time.Time) error
	RecordMerge(ctx context.Context, m MergeRecord) error // ErrConflict if the change was merged already
	// ListMerges returns the merges Plumbix pushed to a branch, oldest first.
	ListMerges(ctx context.Context, repoID, branch string) ([]MergeRecord, error)
}
