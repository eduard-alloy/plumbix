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
	PlumbixBranch bool `json:"plumbix_branch,omitempty"`
	// CriteriaSet names the set the change took its criteria from, frozen
	// at submission (WU-066, A43), or the set it named but could not use.
	CriteriaSet *CriteriaSetRef `json:"criteria_set,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// CriteriaSet is acceptance criteria written with the specification,
// before the code, by the person who owns the intent (WU-066, A43): a
// client such as Kai creates one when a task's spec is settled, and a
// change or a pull request names it instead of stating criteria of its
// own. Once a change has used a set, the set is frozen.
type CriteriaSet struct {
	ID    string `json:"id"`
	Task  string `json:"task"` // an opaque reference, such as kai:<project>/<ref>
	Title string `json:"title,omitempty"`
	Owner string `json:"owner"` // the person publishing, as the client knew them
	// Source says which tool published the set, when it says.
	Source   *CriteriaSource `json:"source,omitempty"`
	Criteria []Criterion     `json:"criteria"`
	// UsedBy lists the changes that currently take their criteria from
	// the set. Frozen is set the first time a change does and never
	// cleared, so a set stays frozen when a pull request later drops its
	// reference.
	UsedBy    []string  `json:"used_by"`
	Frozen    bool      `json:"frozen"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CriteriaSource names the tool a criteria set came from.
type CriteriaSource struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

// CriteriaSetInput is what a client sends to create or replace a set.
type CriteriaSetInput struct {
	Task     string          `json:"task"`
	Title    string          `json:"title"`
	Owner    string          `json:"owner"`
	Source   *CriteriaSource `json:"source"`
	Criteria []Criterion     `json:"criteria"`
}

// CriteriaSetRef is what a change keeps of the set it named: who owned
// it and which task it belonged to, or, when the set could not be used,
// why (Problem), the change then carrying the criteria it stated itself.
type CriteriaSetRef struct {
	ID      string `json:"id"`
	Owner   string `json:"owner,omitempty"`
	Task    string `json:"task,omitempty"`
	Problem string `json:"problem,omitempty"`
	// Version is the set's updated_at when its criteria were read; the
	// store writes the reference only if the set still has it, so a set
	// edited between the read and the write cannot be recorded as used
	// with criteria it no longer holds.
	Version time.Time `json:"-"`
}

// ErrCriteriaSetUsed means a change took its criteria from the set, so
// the set can no longer change.
var ErrCriteriaSetUsed = errors.New("criteria set used by a change")

// ErrCriteriaSetChanged means the set was edited between the moment its
// criteria were read for a change and the moment the change was written;
// the caller reads the set again and submits again.
var ErrCriteriaSetChanged = errors.New("criteria set changed while the change was being written; read it again")

// MaxTaskRef bounds a criteria set's task reference.
const MaxTaskRef = 200

// HumanApproval records that a person approved a change, who and why.
type HumanApproval struct {
	Reason string    `json:"reason"`
	Head   string    `json:"head"` // the validated commit the person approved
	At     time.Time `json:"at"`
	// By is the person as the surface knew them: the name given to the
	// Approve action, or the GitHub login of an approving review (WU-060).
	By string `json:"by,omitempty"`
	// Source is the surface the approval came through: plumbix (the
	// Approve action on the API, CLI, web or MCP tool) or github_review.
	Source string `json:"source,omitempty"`
}

// Approval sources (WU-060).
const (
	ApprovalSourcePlumbix      = "plumbix"
	ApprovalSourceGitHubReview = "github_review"
)

// Approver names the person behind an approval and the surface it came
// through; an empty Source is plumbix.
type Approver struct {
	By     string
	Source string
}

// Merge says how a change landed on its target branch.
type Merge struct {
	Commit   string `json:"commit"`   // the commit now at the target's head
	Previous string `json:"previous"` // the target's head before the merge
	Strategy string `json:"strategy"`
	RunID    string `json:"run_id"` // the approved run that was merged; empty when GitHub merged before any run
	// MergedBy is the GitHub login that merged a pull request; empty for
	// Plumbix's own merges.
	MergedBy string `json:"merged_by,omitempty"`
	// AutoMerge says GitHub merged on the requester's behalf once the gate
	// was green: MergedBy is who requested auto-merge, not who decided
	// (WU-063). False for a merge a person pressed and for Plumbix's own.
	AutoMerge bool      `json:"auto_merge,omitempty"`
	MergedAt  time.Time `json:"merged_at"`
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
	// Note says why a pull request's item was not bound to the steps it
	// named (WU-062): an unknown step, or a command, which a pull request
	// cannot run. Shown beside the criterion, never dropped.
	Note string `json:"note,omitempty"`
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
	// UpdateChangeText rewrites a change's title, intent, criteria and
	// criteria-set reference, as a pull request's edit asks (WU-062,
	// WU-066); refused for a final change.
	UpdateChangeText(ctx context.Context, id, title, intent string, criteria []Criterion, set *CriteriaSetRef, at time.Time) error
	// Criteria sets (WU-066). GetCriteriaSet fills UsedBy; UpdateCriteriaSet
	// is ErrCriteriaSetUsed once a change has used the set.
	CreateCriteriaSet(ctx context.Context, cs CriteriaSet) error
	UpdateCriteriaSet(ctx context.Context, cs CriteriaSet) error
	GetCriteriaSet(ctx context.Context, id string) (CriteriaSet, error)
	ListCriteriaSets(ctx context.Context, task string) ([]CriteriaSet, error)
	// ApproveByPerson records a person's approval at head and moves the
	// change from state from to approved. ErrConflict if it left state from.
	ApproveByPerson(ctx context.Context, id string, from State, approval HumanApproval) error
	RecordMerge(ctx context.Context, m MergeRecord) error // ErrConflict if the change was merged already
	// ListMerges returns the merges Plumbix pushed to a branch, oldest first.
	ListMerges(ctx context.Context, repoID, branch string) ([]MergeRecord, error)
}
