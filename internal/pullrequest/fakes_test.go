package pullrequest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/store"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var ctx = context.Background()

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// fakeGitHub is a GitHub that answers from tables and records the check
// runs posted to it. It is also a host.Host so a Registry can carry it.
type fakeGitHub struct {
	mu          sync.Mutex
	prs         map[int]host.PullRequest
	reviews     map[int][]host.Review
	required    []string
	known       bool
	requiredErr error
	created     []host.CheckRun
	updated     map[int64]host.CheckRun
	nextID      int64
	calls       []string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{prs: map[int]host.PullRequest{}, reviews: map[int][]host.Review{}, updated: map[int64]host.CheckRun{}, known: true, required: []string{GateName}}
}

func (f *fakeGitHub) Kind() string                         { return change.HostGitHub }
func (f *fakeGitHub) Auth(change.Repo) (gitops.Auth, bool) { return gitops.Auth{}, false }
func (f *fakeGitHub) TokenExpiry(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (f *fakeGitHub) ListPullRequests(_ context.Context, _ change.Repo, state string) ([]host.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "list "+state)
	var out []host.PullRequest
	for _, pr := range f.prs {
		if pr.State == state {
			out = append(out, pr)
		}
	}
	return out, nil
}

func (f *fakeGitHub) GetPullRequest(_ context.Context, _ change.Repo, n int) (host.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("get %d", n))
	pr, ok := f.prs[n]
	if !ok {
		return pr, &host.StatusError{Status: 404, Path: "pulls"}
	}
	return pr, nil
}

func (f *fakeGitHub) ListReviews(_ context.Context, _ change.Repo, n int) ([]host.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("reviews %d", n))
	return f.reviews[n], nil
}

func (f *fakeGitHub) CreateCheckRun(_ context.Context, _ change.Repo, c host.CheckRun) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.created = append(f.created, c)
	return f.nextID, nil
}

func (f *fakeGitHub) UpdateCheckRun(_ context.Context, _ change.Repo, id int64, c host.CheckRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated[id] = c
	return nil
}

func (f *fakeGitHub) RequiredChecks(context.Context, change.Repo, string) ([]string, bool, error) {
	return f.required, f.known, f.requiredErr
}

// checks returns the created check runs by name.
func (f *fakeGitHub) checks(name string) []host.CheckRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.CheckRun
	for _, c := range f.created {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// fakeEngine runs no pipeline: Validate finishes a run at once with the
// configured outcome and head, so intake can be tested on the store alone.
type fakeEngine struct {
	db        *store.DB
	outcome   change.State
	head      string
	steps     []string // the pipeline's steps at any branch head; nil means no pipeline file
	pipeErr   error
	validated []string
	merges    []engine.ExternalMerge
	approvals []string
	n         int
}

// PipelineFile answers with the fake's steps as a parsed pipeline.
func (f *fakeEngine) PipelineFile(ctx context.Context, repoID, branch string) (engine.PipelineFileView, error) {
	if f.pipeErr != nil {
		return engine.PipelineFileView{}, f.pipeErr
	}
	v := engine.PipelineFileView{Branch: branch, SHA: f.head}
	if f.steps == nil {
		return v, nil
	}
	v.Found = true
	v.Pipeline = &pipeline.Pipeline{}
	for _, name := range f.steps {
		v.Pipeline.Steps = append(v.Pipeline.Steps, pipeline.Step{Name: name})
	}
	return v, nil
}

func (f *fakeEngine) Validate(ctx context.Context, changeID string) (run.Run, error) {
	c, err := f.db.GetChange(ctx, changeID)
	if err != nil {
		return run.Run{}, err
	}
	f.n++
	f.validated = append(f.validated, changeID)
	r := run.Run{ID: fmt.Sprintf("run_%012d", f.n), RepoID: c.RepoID, ChangeID: c.ID, Branch: c.SourceBranch, Iteration: f.n, Status: run.Running, StartedAt: t0, Steps: []run.Step{}}
	if err := f.db.CreateRun(ctx, r); err != nil {
		return r, err
	}
	if err := f.db.SetChangeState(ctx, c.ID, c.State, change.Running, t0); err != nil {
		return r, err
	}
	if err := f.db.SetRunCommits(ctx, r.ID, f.head, "base"); err != nil {
		return r, err
	}
	res := &validation.Result{State: f.outcome, Criteria: []validation.CombinedCriterion{}, Findings: []validation.SourcedFinding{}, Risks: []validation.SourcedRisk{}, Reasons: []string{}, Validators: []validation.ValidatorOutcome{}}
	if err := f.db.FinishRun(ctx, r.ID, run.Finish{Status: run.Finished, Outcome: f.outcome, Result: res, Steps: []run.Step{}, At: t0.Add(time.Minute)}); err != nil {
		return r, err
	}
	return r, f.db.SetChangeState(ctx, c.ID, change.Running, f.outcome, t0.Add(time.Minute))
}

func (f *fakeEngine) ApproveByPerson(ctx context.Context, changeID string, who change.Approver, reason string) (change.Change, error) {
	c, err := f.db.GetChange(ctx, changeID)
	if err != nil {
		return c, err
	}
	if c.State != change.NeedsHuman && c.State != change.Escalated {
		return c, change.ErrInvalidState
	}
	f.approvals = append(f.approvals, reason)
	if err := f.db.ApproveByPerson(ctx, changeID, c.State, change.HumanApproval{Reason: reason, Head: f.head, At: t0, By: who.By, Source: who.Source}); err != nil {
		return c, err
	}
	return f.db.GetChange(ctx, changeID)
}

func (f *fakeEngine) RecordExternalMerge(ctx context.Context, changeID string, m engine.ExternalMerge) (change.Change, error) {
	c, err := f.db.GetChange(ctx, changeID)
	if err != nil {
		return c, err
	}
	if c.State.Final() {
		return c, change.ErrInvalidState
	}
	f.merges = append(f.merges, m)
	if err := f.db.RecordMerge(ctx, change.MergeRecord{ChangeID: c.ID, RepoID: c.RepoID, Branch: c.TargetBranch, Merge: change.Merge{Commit: m.Commit, Previous: "prev", Strategy: change.StrategyGitHub, MergedBy: m.MergedBy, MergedAt: t0}}); err != nil {
		return c, err
	}
	if err := f.db.SetChangeState(ctx, c.ID, c.State, change.Merged, t0); err != nil {
		return c, err
	}
	return f.db.GetChange(ctx, changeID)
}

type harness struct {
	db   *store.DB
	gh   *fakeGitHub
	eng  *fakeEngine
	svc  *change.Service
	s    *Syncer
	repo change.Repo
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := change.NewService(db)
	repo, err := svc.RegisterRepo(ctx, change.RepoInput{Host: change.HostGitHub, URL: "https://github.com/a/b", PullRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	gh := newFakeGitHub()
	eng := &fakeEngine{db: db, outcome: change.NeedsHuman, head: "h1"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Syncer{Hosts: host.Registry{change.HostGitHub: gh}, Store: db, Changes: svc, Engine: eng, Log: log, Now: func() time.Time { return t0 }}
	return &harness{db: db, gh: gh, eng: eng, svc: svc, s: s, repo: repo}
}

// pr is an open pull request from a user on a branch of the repository.
func pr(n int, title, body string) host.PullRequest {
	p := host.PullRequest{Number: n, URL: fmt.Sprintf("https://github.com/a/b/pull/%d", n), Title: title, Body: body, State: "open", Author: host.Actor{Login: "octocat", Type: "User"}}
	p.Head.Branch, p.Head.SHA, p.Head.Repo.FullName = fmt.Sprintf("feat-%d", n), "h1", "a/b"
	p.Base.Branch, p.Base.SHA, p.Base.Repo.FullName = "main", "m1", "a/b"
	return p
}
