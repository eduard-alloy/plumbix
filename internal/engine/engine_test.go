package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/refvalidator"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/settings"
	"github.com/eduard-alloy/plumbix/internal/store"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var ctx = context.Background()

const pipelineYAML = `version: 1
steps:
  - {name: test, image: "golang:1.27", run: "go test ./..."}
  - {name: validate, image: "plumbix-reference-validator:dev", validator: true, needs: [test]}
`

// fakeRunner runs normal steps by script and validator steps through the
// real reference validator, in-process.
type fakeRunner struct {
	mu     sync.Mutex
	exit   map[string]int   // exit code per step name
	block  map[string]bool  // steps that wait for cancellation
	fail   map[string]error // steps whose runner errors
	inputs []validation.Input
	diffs  []string
	jobs   []runner.Job
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{exit: map[string]int{}, block: map[string]bool{}, fail: map[string]error{}}
}

func (f *fakeRunner) Run(ctx context.Context, job runner.Job) (runner.Result, error) {
	f.mu.Lock()
	f.jobs = append(f.jobs, job)
	exit, block, failErr := f.exit[job.Name], f.block[job.Name], f.fail[job.Name]
	f.mu.Unlock()
	if failErr != nil {
		return runner.Result{}, failErr
	}
	if block {
		<-ctx.Done()
		return runner.Result{}, ctx.Err()
	}
	fmtLog(job.Log, "ran "+job.Name)
	var in, out string
	for _, m := range job.Mounts {
		switch m.Target {
		case validation.InPath:
			in = m.Source
		case validation.OutPath:
			out = m.Source
		}
	}
	if out == "" {
		return runner.Result{ExitCode: exit}, nil
	}
	input, err := validation.ReadInput(in)
	if err != nil {
		return runner.Result{}, err
	}
	diff, _ := os.ReadFile(filepath.Join(in, validation.DiffFile))
	f.mu.Lock()
	f.inputs = append(f.inputs, input)
	f.diffs = append(f.diffs, string(diff))
	f.mu.Unlock()
	v, err := refvalidator.Run(ctx, in, job.Workspace, refvalidator.Options{CommandTimeout: 10 * time.Second})
	if err != nil {
		return runner.Result{ExitCode: 1}, nil
	}
	data, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(out, validation.VerdictFile), data, 0o644); err != nil {
		return runner.Result{}, err
	}
	return runner.Result{ExitCode: 0}, nil
}

func fmtLog(w io.Writer, s string) { _, _ = io.WriteString(w, s+"\n") }

type harness struct {
	t      *testing.T
	db     *store.DB
	svc    *change.Service
	eng    *Engine
	fake   *fakeRunner
	repo   *gittest.Repo
	repoID string
	data   string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	return newHarnessWith(t, cfg, "squash")
}

// newHarnessWith is newHarness with the repo's merge strategy.
func newHarnessWith(t *testing.T, cfg Config, strategy string) *harness {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(filepath.Join(data, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, db: db, svc: change.NewService(db), fake: newFakeRunner(), repo: gittest.New(t), data: data}
	cfg.DataDir = data
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = 2
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = time.Minute
	}
	if cfg.MaxFixRounds == 0 {
		cfg.MaxFixRounds = 3
	}
	h.eng = New(db, h.fake, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { h.eng.Shutdown(context.Background()) })
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": pipelineYAML}, "add pipeline")
	// Register through the store directly: the test remote is a local path,
	// which the public repo rules (GitHub or Azure DevOps URLs) don't allow.
	h.repoID = "r_testrepo0001"
	if err := db.CreateRepo(ctx, change.Repo{ID: h.repoID, Host: "github", URL: h.repo.Remote, ProtectedBranches: []string{"main"}, MergeStrategy: strategy, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) submit(branch string) change.Change { return h.submitAs(branch, false) }

// submitAs submits a change on branch; own marks the branch as one Plumbix
// created, as the pipeline editor's endpoint does.
func (h *harness) submitAs(branch string, own bool) change.Change {
	h.t.Helper()
	c, err := h.svc.Submit(ctx, change.ChangeInput{
		RepoID: h.repoID, SourceBranch: branch, TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}}},
		PlumbixBranch:      own,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

// validate starts a run and waits for it to end.
func (h *harness) validate(changeID string) run.Run {
	h.t.Helper()
	r, err := h.eng.Validate(ctx, changeID)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.wait(r.ID)
}

// wait returns the run once it ended and, for a run that moves its change,
// once the change has left running: the engine records the run first and
// moves the change right after, and under load a test that read the state
// in between saw it still running.
func (h *harness) wait(runID string) run.Run {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var r run.Run
	for {
		var err error
		if r, err = h.db.GetRun(ctx, runID); err != nil {
			h.t.Fatal(err)
		}
		if r.Status != run.Running {
			break
		}
		if !time.Now().Before(deadline) {
			h.t.Fatalf("run %s did not finish", runID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.ChangeID == "" || r.Status == run.Cancelled {
		return r
	}
	for time.Now().Before(deadline) {
		if h.state(r.ChangeID) != change.Running {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("change %s still running after run %s ended", r.ChangeID, runID)
	return r
}

func (h *harness) state(id string) change.State {
	h.t.Helper()
	c, err := h.db.GetChange(ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return c.State
}

func TestApprovedChange(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	if r.Status != run.Finished || r.Outcome != change.Approved || r.Iteration != 1 {
		t.Fatalf("run = %+v", r)
	}
	if got := h.state(c.ID); got != change.Approved {
		t.Errorf("change state = %s", got)
	}
	if len(r.Steps) != 2 || r.Steps[0].Name != "test" || r.Steps[0].Status != "succeeded" || !r.Steps[1].Validator {
		t.Errorf("steps = %+v", r.Steps)
	}
	if r.Head == "" || r.Base == "" || r.Result == nil || r.Result.State != change.Approved {
		t.Errorf("commits %q %q result %+v", r.Head, r.Base, r.Result)
	}
	in := h.fake.inputs[0]
	if len(in.ChangedFiles) != 1 || in.ChangedFiles[0] != "app.go" || in.Iteration != 1 || in.Commits.Head != r.Head {
		t.Errorf("validator input = %+v", in)
	}
	if !strings.Contains(h.fake.diffs[0], "+package app") {
		t.Errorf("diff = %q", h.fake.diffs[0])
	}
	log, err := os.ReadFile(h.eng.LogPath(r.ID, "test"))
	if err != nil || !strings.Contains(string(log), "ran test") {
		t.Errorf("log = %q, %v", log, err)
	}
	for _, j := range h.fake.jobs {
		if j.Name == "validate" && !j.WorkspaceReadOnly {
			t.Error("validator got a writable workspace")
		}
		if j.Name == "test" && j.WorkspaceReadOnly {
			t.Error("normal step got a read-only workspace")
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(h.data, "work")); len(entries) != 0 {
		t.Errorf("work folder not cleaned up: %v", entries)
	}
}

func TestFixThenApproveOnIterationTwo(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	h.fake.exit["test"] = 1
	r1 := h.validate(c.ID)
	if r1.Outcome != change.FixRequested || len(r1.Result.Findings) == 0 || h.state(c.ID) != change.FixRequested {
		t.Fatalf("iteration 1 = %+v", r1)
	}
	h.repo.Commit("feat", map[string]string{"app.go": "package app // fixed\n"}, "fix")
	h.fake.exit["test"] = 0
	r2 := h.validate(c.ID)
	if r2.Iteration != 2 || r2.Outcome != change.Approved || r2.Head == r1.Head {
		t.Fatalf("iteration 2 = %+v", r2)
	}
	in := h.fake.inputs[1]
	if in.Iteration != 2 || len(in.PreviousFindings) == 0 {
		t.Errorf("iteration 2 input: iteration %d, previous findings %v", in.Iteration, in.PreviousFindings)
	}
}

func TestEscalationAfterFixRounds(t *testing.T) {
	h := newHarness(t, Config{MaxFixRounds: 1})
	h.repo.Commit("feat", map[string]string{"app.go": "x\n"}, "feature")
	c := h.submit("feat")
	h.fake.exit["test"] = 1
	if r := h.validate(c.ID); r.Outcome != change.FixRequested {
		t.Fatalf("iteration 1 outcome = %s", r.Outcome)
	}
	r := h.validate(c.ID)
	if r.Outcome != change.Escalated || h.state(c.ID) != change.Escalated || !strings.Contains(strings.Join(r.Result.Reasons, " "), "fix round") {
		t.Fatalf("iteration 2 = %+v reasons %v", r, r.Result.Reasons)
	}
}

func TestRequiredValidatorsComeFromTarget(t *testing.T) {
	h := newHarness(t, Config{})
	noValidator := "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n"
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": noValidator}, "drop the validator")
	c := h.submit("feat")
	r := h.validate(c.ID)
	if r.Outcome != change.NeedsHuman {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	reasons := strings.Join(r.Result.Reasons, " ")
	if !strings.Contains(reasons, "required validator validate") || !strings.Contains(reasons, ".plumbix/") {
		t.Errorf("reasons = %v", r.Result.Reasons)
	}
}

func TestInfrastructureErrors(t *testing.T) {
	cases := map[string]func(h *harness) change.Change{
		"no pipeline file": func(h *harness) change.Change {
			h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": ""}, "remove pipeline")
			return h.submit("feat")
		},
		"invalid pipeline": func(h *harness) change.Change {
			h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": "version: 2\n"}, "break pipeline")
			return h.submit("feat")
		},
		"unknown branch": func(h *harness) change.Change { return h.submit("never-pushed") },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Config{})
			c := setup(h)
			r := h.validate(c.ID)
			if r.Status != run.Errored || r.Error == "" || r.Outcome != change.NeedsHuman {
				t.Fatalf("run = %+v", r)
			}
			if got := h.state(c.ID); got != change.NeedsHuman {
				t.Errorf("change state = %s", got)
			}
		})
	}
}

func TestRunnerErrorIsNotAVerdict(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	c := h.submit("feat")
	h.fake.fail["test"] = errors.New("docker engine unavailable")
	r := h.validate(c.ID)
	if r.Outcome != change.NeedsHuman || !strings.Contains(strings.Join(r.Result.Reasons, " "), "docker engine unavailable") {
		t.Fatalf("outcome = %s, reasons %v; an infrastructure error must not become a fix request", r.Outcome, r.Result.Reasons)
	}
	if r.Steps[0].Status != "errored" || !strings.Contains(r.Steps[0].Error, "docker engine unavailable") {
		t.Errorf("steps = %+v", r.Steps)
	}
}

func TestRevalidateCancelsRunningRun(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	c := h.submit("feat")
	h.fake.mu.Lock()
	h.fake.block["test"] = true
	h.fake.mu.Unlock()
	first, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	h.fake.mu.Lock()
	h.fake.block["test"] = false
	h.fake.mu.Unlock()
	second := h.validate(c.ID)
	old := h.wait(first.ID)
	if old.Status != run.Cancelled || second.Outcome != change.Approved || h.state(c.ID) != change.Approved {
		t.Fatalf("first %s, second %s, change %s", old.Status, second.Outcome, h.state(c.ID))
	}
}

func TestFinalChangesCannotBeValidated(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	c := h.submit("feat")
	if _, err := h.svc.Close(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Validate(ctx, c.ID); !errors.Is(err, change.ErrInvalidState) {
		t.Fatalf("err = %v", err)
	}
}

func TestClosedDuringRunStaysClosed(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	c := h.submit("feat")
	h.fake.mu.Lock()
	h.fake.block["test"] = true
	h.fake.mu.Unlock()
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := h.svc.Close(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	h.eng.Shutdown(context.Background())
	h.wait(r.ID)
	if got := h.state(c.ID); got != change.Closed {
		t.Errorf("state = %s, want closed", got)
	}
}

func TestShutdownMarksRunsInterrupted(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	c := h.submit("feat")
	h.fake.mu.Lock()
	h.fake.block["test"] = true
	h.fake.mu.Unlock()
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	h.eng.Shutdown(context.Background())
	got := h.wait(r.ID)
	if got.Status != run.Errored || !strings.Contains(got.Error, "server") || h.state(c.ID) != change.NeedsHuman {
		t.Fatalf("run %+v, change %s", got, h.state(c.ID))
	}
}

func TestRecoverMarksStaleRuns(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.submit("feat")
	if err := h.db.CreateRun(ctx, run.Run{ID: "run_stale0000001", RepoID: h.repoID, ChangeID: c.ID, Iteration: 1, Status: run.Running, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetChangeState(ctx, c.ID, change.Open, change.Running, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := h.db.GetRun(ctx, "run_stale0000001")
	if r.Status != run.Errored || h.state(c.ID) != change.NeedsHuman {
		t.Fatalf("run %s, change %s", r.Status, h.state(c.ID))
	}
}

func TestLogPathRejectsTraversal(t *testing.T) {
	h := newHarness(t, Config{})
	if p := h.eng.LogPath("run_x", "../../etc"); p != "" {
		t.Errorf("LogPath accepted a traversal: %s", p)
	}
	if p := h.eng.LogPath("../run", "test"); p != "" {
		t.Errorf("LogPath accepted a bad run id: %s", p)
	}
}

func TestRunBranchSkipsValidators(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("ci-only", map[string]string{"x.go": "package x\n"}, "push without a change")
	r, err := h.eng.RunBranch(ctx, h.repoID, "ci-only")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(r.ID)
	if got.Status != run.Finished || got.Outcome != "" || got.Result != nil || got.ChangeID != "" || got.Branch != "ci-only" {
		t.Fatalf("run = %+v", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "test" || got.Head == "" {
		t.Errorf("steps = %+v head %q", got.Steps, got.Head)
	}
	for _, j := range h.fake.jobs {
		if j.Name == "validate" {
			t.Error("a validator ran in a plain run")
		}
	}
}

func TestRunBranchErrors(t *testing.T) {
	h := newHarness(t, Config{})
	r, err := h.eng.RunBranch(ctx, h.repoID, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.wait(r.ID); got.Status != run.Errored || got.Error == "" || got.Outcome != "" {
		t.Fatalf("run = %+v; a plain run has no outcome", got)
	}
	if _, err := h.eng.RunBranch(ctx, "r_nope", "main"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown repo: %v", err)
	}
}

func TestFetchUsesHostCredentials(t *testing.T) {
	const token = "host_engine_secret_42"
	for name, tc := range map[string]struct {
		kind    string
		adapter func(token string) host.Host
		token   string
		outcome change.State
	}{
		"github right token":      {change.HostGitHub, func(t string) host.Host { return &host.GitHub{Token: t} }, token, change.Approved},
		"github wrong token":      {change.HostGitHub, func(t string) host.Host { return &host.GitHub{Token: t} }, "nope", change.NeedsHuman},
		"azuredevops right token": {change.HostAzureDevOps, func(t string) host.Host { return &host.AzureDevOps{Token: t} }, token, change.Approved},
		"azuredevops wrong token": {change.HostAzureDevOps, func(t string) host.Host { return &host.AzureDevOps{Token: t} }, "nope", change.NeedsHuman},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := tc.adapter(tc.token)
			h := newHarness(t, Config{Hosts: host.Registry{tc.kind: adapter}})
			h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
			repo := change.Repo{ID: "r_httprepo0001", Host: tc.kind, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}
			auth, _ := adapter.Auth(repo)
			repo.URL = h.repo.ServeHTTP(auth.User, token)
			if err := h.db.CreateRepo(ctx, repo); err != nil {
				t.Fatal(err)
			}
			h.repoID = repo.ID
			r := h.validate(h.submit("feat").ID)
			if r.Outcome != tc.outcome {
				t.Fatalf("outcome = %s (%s)", r.Outcome, r.Error)
			}
			if strings.Contains(r.Error, token) {
				t.Fatal("run error leaks the token")
			}
		})
	}
}

const richPipelineYAML = `version: 1
steps:
  - {name: test, image: "golang:1.27", run: ["go build ./...", "go test ./..."]}
  - {name: flaky, image: "golang:1.27", run: "flaky", retries: 2, allow_failure: true}
  - {name: api, image: "golang:1.27", run: "check api", when: {paths: ["api/**"]}}
  - {name: after, image: "golang:1.27", run: "after", needs: [flaky, api]}
  - {name: validate, image: "plumbix-reference-validator:dev", validator: true, needs: [test, after]}
stages:
  checks: [flaky, api, after]
`

func TestChangeRunRecordsRetriesAllowancesSkipsAndStages(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.exit["flaky"] = 1
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": richPipelineYAML, "app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r := h.wait(h.validate(c.ID).ID)
	by := map[string]run.Step{}
	for _, s := range r.Steps {
		by[s.Name] = s
	}
	if s := by["flaky"]; s.Status != "failed" || s.Attempts != 3 || !s.AllowedFailure || s.Stage != "checks" {
		t.Errorf("flaky = %+v", s)
	}
	if s := by["api"]; s.Status != "skipped" || s.Reason != "paths" || s.Stage != "checks" {
		t.Errorf("api = %+v", s)
	}
	if s := by["after"]; s.Status != "succeeded" || s.Stage != "checks" {
		t.Errorf("after = %+v: an allowed failure and a path skip must not skip dependents", s)
	}
	if s := by["test"]; s.Status != "succeeded" || s.Attempts != 0 || s.Stage != "" {
		t.Errorf("test = %+v", s)
	}
	ran := 0
	for _, j := range h.fake.jobs {
		if j.Name == "flaky" {
			ran++
		}
		if j.Name == "test" && j.Script != "set -e\ngo build ./...\ngo test ./..." {
			t.Errorf("test script = %q", j.Script)
		}
		if j.Name == "api" {
			t.Error("api ran although no api/ file changed")
		}
	}
	if ran != 3 {
		t.Errorf("flaky ran %d times, want 3", ran)
	}
	log, _ := os.ReadFile(h.eng.LogPath(r.ID, "flaky"))
	if !strings.Contains(string(log), "attempt 3 of 3") {
		t.Errorf("flaky log = %q", log)
	}
	if len(h.fake.inputs) == 0 {
		t.Fatal("the validator did not run")
	}
}

func TestBranchRunRunsConditionalSteps(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("ci-only", map[string]string{".plumbix/pipeline.yaml": richPipelineYAML, "x.go": "package x\n"}, "push without a change")
	r, err := h.eng.RunBranch(ctx, h.repoID, "ci-only")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(r.ID)
	by := map[string]run.Step{}
	for _, s := range got.Steps {
		by[s.Name] = s
	}
	if s := by["api"]; s.Status != "succeeded" || s.Reason != "" {
		t.Errorf("api = %+v: a plain branch run has no changed files to compare, so conditional steps run", s)
	}
	if s := by["flaky"]; s.Status != "succeeded" || s.Attempts != 0 {
		t.Errorf("flaky = %+v", s)
	}
}

func TestPipelineFileReadsTheBranchHead(t *testing.T) {
	h := newHarness(t, Config{})
	v, err := h.eng.PipelineFile(ctx, h.repoID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || v.Pipeline == nil || len(v.Pipeline.Steps) != 2 || string(v.Content) != pipelineYAML || v.SHA == "" || len(v.Problems) != 0 {
		t.Errorf("view = %+v", v)
	}
	h.repo.Commit("broken", map[string]string{".plumbix/pipeline.yaml": "version: 2\n"}, "break")
	v, err = h.eng.PipelineFile(ctx, h.repoID, "broken")
	if err != nil || !v.Found || v.Pipeline != nil || len(v.Problems) == 0 || v.Problems[0].Path != "version" {
		t.Errorf("broken view = %+v %v", v, err)
	}
	h.repo.Commit("bare", map[string]string{".plumbix/pipeline.yaml": "", "x.txt": "x\n"}, "no pipeline")
	v, err = h.eng.PipelineFile(ctx, h.repoID, "bare")
	if err != nil || v.Found || v.Pipeline != nil || len(v.Problems) != 1 {
		t.Errorf("missing view = %+v %v", v, err)
	}
	if _, err := h.eng.PipelineFile(ctx, h.repoID, "nope"); !errors.Is(err, gitops.ErrUnknownBranch) {
		t.Errorf("unknown branch err = %v", err)
	}
}

func TestProposeFilePushesANewBranchOnly(t *testing.T) {
	h := newHarness(t, Config{})
	content := []byte(pipelineYAML + "  - {name: lint, image: \"golang:1.27\", run: \"go vet ./...\"}\n")
	p := Proposal{TargetBranch: "main", Branch: "pipeline/add-lint", Path: PipelineFile, Content: content, Message: "Pipeline: add lint"}
	sha, err := h.eng.ProposeFile(ctx, h.repoID, p)
	if err != nil {
		t.Fatal(err)
	}
	heads, _ := h.eng.ListHeads(ctx, change.Repo{ID: h.repoID, Host: "github", URL: h.repo.Remote})
	if heads["pipeline/add-lint"] != sha {
		t.Errorf("remote branch = %s, want %s", heads["pipeline/add-lint"], sha)
	}
	main := h.repo.Commit("main", map[string]string{"untouched.txt": "x\n"}, "main goes on")
	_ = main
	if _, err := h.eng.ProposeFile(ctx, h.repoID, p); !errors.Is(err, gitops.ErrBranchExists) {
		t.Errorf("second proposal on the same branch: %v", err)
	}
	if _, err := h.eng.ProposeFile(ctx, h.repoID, Proposal{TargetBranch: "main", Branch: "main", Path: PipelineFile, Content: content, Message: "m"}); !errors.Is(err, gitops.ErrBranchExists) {
		t.Errorf("proposal onto main: %v", err)
	}
	if _, err := h.eng.ProposeFile(ctx, h.repoID, Proposal{TargetBranch: "nope", Branch: "pipeline/x", Path: PipelineFile, Content: content, Message: "m"}); !errors.Is(err, gitops.ErrUnknownBranch) {
		t.Errorf("unknown target: %v", err)
	}
	// The proposed branch validates like any other and, touching .plumbix/, needs a person.
	c := h.submit("pipeline/add-lint")
	r := h.wait(h.validate(c.ID).ID)
	if r.Outcome != change.NeedsHuman {
		t.Errorf("outcome = %s, want needs_human for a pipeline edit", r.Outcome)
	}
}

func TestAPersonApprovesAnEscalatedChangeAndItMerges(t *testing.T) {
	h := newHarness(t, Config{MaxFixRounds: 1})
	h.repo.Commit("feat", map[string]string{"app.go": "x\n"}, "feature")
	c := h.submit("feat")
	h.fake.exit["test"] = 1
	h.validate(c.ID)
	r := h.validate(c.ID)
	if r.Outcome != change.Escalated {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "  "); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("empty reason: %v", err)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, strings.Repeat("x", MaxApprovalReason+1)); err == nil {
		t.Error("an overlong reason was accepted")
	}
	got, err := h.eng.ApproveByPerson(ctx, c.ID, "the failing test is a known flake; fixed in the next change")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != change.Approved || got.HumanApproval == nil || got.HumanApproval.Head != r.Head || got.HumanApproval.Reason == "" {
		t.Fatalf("approved change = %+v %+v", got.State, got.HumanApproval)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "again"); !errors.Is(err, change.ErrInvalidState) {
		t.Errorf("approving an approved change: %v", err)
	}
	merged, err := h.eng.Merge(ctx, c.ID)
	if err != nil {
		t.Fatalf("merge after a person's approval: %v", err)
	}
	if merged.State != change.Merged || merged.Merge == nil || merged.Merge.RunID != r.ID {
		t.Errorf("merged = %+v %+v", merged.State, merged.Merge)
	}
}

func TestApprovalRefusalsAndANewRunEndsIt(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"app.go": "x\n"}, "feature")
	c := h.submit("feat")
	// No run yet: open, so not a state a person approves.
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "why"); !errors.Is(err, change.ErrInvalidState) {
		t.Errorf("open change: %v", err)
	}
	if _, err := h.eng.ApproveByPerson(ctx, "c_nope", "why"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": pipelineYAML + "  - {name: lint, image: x, run: x}\n"}, "pipeline edit")
	r := h.validate(c.ID)
	if r.Outcome != change.NeedsHuman {
		t.Fatalf("outcome = %s, want needs_human for a .plumbix/ edit", r.Outcome)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "reviewed the pipeline edit by hand"); err != nil {
		t.Fatal(err)
	}
	// A new push starts a new run, which ends the approval.
	h.repo.Commit("feat", map[string]string{"more.go": "y\n"}, "more")
	again := h.validate(c.ID)
	fresh, _ := h.svc.Get(ctx, c.ID)
	if fresh.HumanApproval != nil || again.Outcome != change.NeedsHuman {
		t.Errorf("after a new run: approval %+v outcome %s", fresh.HumanApproval, again.Outcome)
	}
	// Merge refuses: the latest run is not an approval and no person approved its head.
	if _, err := h.eng.Merge(ctx, c.ID); !errors.Is(err, change.ErrInvalidState) {
		t.Errorf("merge: %v", err)
	}
}

func TestApprovalRefusesRejectedAndUnfinishedRuns(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": pipelineYAML + "  - {name: lint, image: x, run: x}\n"}, "pipeline edit")
	c := h.submit("feat")
	if r := h.validate(c.ID); r.Outcome != change.NeedsHuman {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	// Rejected is the validators' explicit no: a person cannot approve it.
	if err := h.db.SetChangeState(ctx, c.ID, change.NeedsHuman, change.Rejected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "override"); !errors.Is(err, change.ErrInvalidState) || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("approving a rejected change: %v", err)
	}
	if err := h.db.SetChangeState(ctx, c.ID, change.Rejected, change.NeedsHuman, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The latest run errored (the pipeline file broke), so there is no judged head to approve.
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": "version: 2\n"}, "break pipeline")
	if r := h.validate(c.ID); r.Status != run.Errored {
		t.Fatalf("run = %+v", r)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "fine"); !errors.Is(err, change.ErrInvalidState) || !strings.Contains(err.Error(), "finished run") {
		t.Errorf("approving after an errored run: %v", err)
	}
}

func TestRefreshBringsTheTargetIntoAPlumbixBranchAndRevalidates(t *testing.T) {
	h := newHarness(t, Config{})
	content := []byte(pipelineYAML + "  - {name: lint, image: \"golang:1.27\", run: \"go vet ./...\"}\n")
	if _, err := h.eng.ProposeFile(ctx, h.repoID, Proposal{TargetBranch: "main", Branch: "pipeline/add-lint", Path: PipelineFile, Content: content, Message: "Pipeline: add lint"}); err != nil {
		t.Fatal(err)
	}
	c := h.submitAs("pipeline/add-lint", true)
	r1 := h.validate(c.ID)
	if r1.Outcome != change.NeedsHuman {
		t.Fatalf("outcome = %s", r1.Outcome)
	}
	if _, err := h.eng.Refresh(ctx, c.ID); !errors.Is(err, ErrAlreadyCurrent) {
		t.Errorf("refresh while current: %v, want ErrAlreadyCurrent", err)
	}
	// A person approves, then main moves: the merge would be refused with target_moved.
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "pipeline edit read by hand"); err != nil {
		t.Fatal(err)
	}
	main2 := h.repo.Commit("main", map[string]string{"untouched.txt": "x\n"}, "main goes on")
	if _, err := h.eng.Merge(ctx, c.ID); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("merge after main moved: %v", err)
	}
	before, _ := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	got, err := h.eng.Refresh(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != change.Running || got.HumanApproval != nil {
		t.Errorf("after refresh: state %s approval %+v; want running with the approval cleared", got.State, got.HumanApproval)
	}
	repo := change.Repo{ID: h.repoID, Host: "github", URL: h.repo.Remote}
	heads, _ := h.eng.ListHeads(ctx, repo)
	tip, err := h.eng.Commits(ctx, repo, "", heads["pipeline/add-lint"])
	if err != nil || len(tip) != 1 {
		t.Fatalf("tip = %+v %v", tip, err)
	}
	if len(tip[0].Parents) != 2 || tip[0].Parents[0] != r1.Head || tip[0].Parents[1] != main2 || tip[0].Author != gitops.CommitterName {
		t.Errorf("branch tip = %+v; want a Plumbix merge of %s and %s", tip[0], short(r1.Head), short(main2))
	}
	after, _ := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	var refreshed *events.Event
	for i := len(before); i < len(after); i++ {
		if after[i].Type == events.ChangeRefreshed {
			refreshed = &after[i]
		}
	}
	if refreshed == nil {
		t.Fatalf("no change.refreshed event among %+v", after[len(before):])
	}
	raw, _ := json.Marshal(refreshed.Payload)
	var p events.ChangeRefreshedPayload
	if json.Unmarshal(raw, &p) != nil || p.FromHead != r1.Head || p.Head != tip[0].SHA || p.TargetHead != main2 {
		t.Errorf("payload = %s", raw)
	}
	runs, _ := h.db.ListRuns(ctx, c.ID)
	r2 := h.wait(runs[len(runs)-1].ID)
	if r2.Iteration != 2 || r2.Base != main2 || r2.Head != tip[0].SHA || r2.Outcome != change.NeedsHuman {
		t.Errorf("new run = iteration %d base %s head %s outcome %s", r2.Iteration, short(r2.Base), short(r2.Head), r2.Outcome)
	}
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "still fine after the refresh"); err != nil {
		t.Fatal(err)
	}
	merged, err := h.eng.Merge(ctx, c.ID)
	if err != nil || merged.State != change.Merged {
		t.Errorf("merge after refresh: %v %s", err, merged.State)
	}
}

func TestRefreshRefusesBranchesThatAreNotPlumbixsAndConflicts(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.eng.Refresh(ctx, "c_nope"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}
	// An agent's branch: the flag is off, whatever the commits say.
	h.repo.Commit("feat", map[string]string{"app.go": "x\n"}, "feature")
	theirs := h.submit("feat")
	h.validate(theirs.ID)
	h.repo.Commit("main", map[string]string{"other.txt": "x\n"}, "main moves")
	if _, err := h.eng.Refresh(ctx, theirs.ID); !errors.Is(err, ErrNotPlumbixBranch) {
		t.Errorf("agent branch: %v, want ErrNotPlumbixBranch", err)
	}
	// Plumbix created the branch, but someone pushed a commit of their own onto it.
	content := []byte(pipelineYAML + "  - {name: lint, image: x, run: x}\n")
	if _, err := h.eng.ProposeFile(ctx, h.repoID, Proposal{TargetBranch: "main", Branch: "pipeline/touched", Path: PipelineFile, Content: content, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	h.repo.Commit("pipeline/touched", map[string]string{"hand.txt": "edited by hand\n"}, "by hand")
	touched := h.submitAs("pipeline/touched", true)
	h.validate(touched.ID)
	h.repo.Commit("main", map[string]string{"other.txt": "y\n"}, "main moves again")
	if _, err := h.eng.Refresh(ctx, touched.ID); !errors.Is(err, ErrNotPlumbixBranch) {
		t.Errorf("touched branch: %v, want ErrNotPlumbixBranch", err)
	}
	// Both sides edit the pipeline file: no merge, nothing pushed, nothing started.
	if _, err := h.eng.ProposeFile(ctx, h.repoID, Proposal{TargetBranch: "main", Branch: "pipeline/clash", Path: PipelineFile, Content: content, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	clash := h.submitAs("pipeline/clash", true)
	r := h.validate(clash.ID)
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": pipelineYAML + "  - {name: web, image: x, run: x}\n"}, "main edits the pipeline too")
	if _, err := h.eng.Refresh(ctx, clash.ID); !errors.Is(err, gitops.ErrMergeConflict) || !strings.Contains(err.Error(), PipelineFile) {
		t.Errorf("conflict: %v, want ErrMergeConflict naming the file", err)
	}
	heads, _ := h.eng.ListHeads(ctx, change.Repo{ID: h.repoID, Host: "github", URL: h.repo.Remote})
	if heads["pipeline/clash"] != r.Head {
		t.Errorf("branch moved to %s after a refused refresh", short(heads["pipeline/clash"]))
	}
	if got, _ := h.db.GetChange(ctx, clash.ID); got.State != change.NeedsHuman {
		t.Errorf("state after a refused refresh = %s", got.State)
	}
	// Final changes are left alone.
	if err := h.db.SetChangeState(ctx, clash.ID, change.NeedsHuman, change.Closed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Refresh(ctx, clash.ID); !errors.Is(err, change.ErrInvalidState) {
		t.Errorf("closed change: %v, want ErrInvalidState", err)
	}
}

// approvingVerdict is a minimal valid verdict that approves AC-1.
const approvingVerdict = `{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`

const externalYAML = `version: 1
steps:
  - {name: test, image: "golang:1.27", run: "go test ./..."}
  - {name: validate, image: "plumbix-reference-validator:dev", validator: true, needs: [test]}
  - {name: ai-review, validator: true, external: true, needs: [test], timeout: 20s}
`

// claimSoon polls until a review can be claimed, as plumbix review --watch would.
func (h *harness) claimSoon(reviewer string) review.Brief {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, ok, err := h.eng.ClaimReview(ctx, "", reviewer)
		if err != nil {
			h.t.Fatal(err)
		}
		if ok {
			return b
		}
		if time.Now().After(deadline) {
			h.t.Fatal("no review was published")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestExternalValidatorPublishesABriefAndTakesThePostedVerdict(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalYAML}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := h.claimSoon("kai")
	time.Sleep(2 * reviewPoll) // let the step log kai's claim before anything else happens
	if b.RunID != r.ID || b.ChangeID != c.ID || b.Step != "ai-review" || b.Token == "" || b.Head == "" || b.Base == "" || b.RepoURL != h.repo.Remote || b.Iteration != 1 {
		t.Errorf("brief = %+v", b)
	}
	if b.Tools != review.ToolsReadOnly || !strings.Contains(b.Prompt.System, "independent validator") || !strings.Contains(b.Prompt.User, "app.go") || b.Schema["type"] != "object" {
		t.Errorf("brief content: tools %q, system %d chars, user has app.go %v, schema %v", b.Tools, len(b.Prompt.System), strings.Contains(b.Prompt.User, "app.go"), b.Schema["type"])
	}
	if want := []string{"AC-1"}; !reflect.DeepEqual(b.Criteria, want) {
		t.Errorf("criteria = %v, want %v", b.Criteria, want)
	}
	if until := time.Until(b.ExpiresAt); until < 10*time.Second || until > 21*time.Second {
		t.Errorf("expires in %s, want about the step's 20s timeout", until)
	}
	// Nobody else can take it now, and a second reviewer gets nothing.
	if _, ok, _ := h.eng.ClaimReview(ctx, "", "otto"); ok {
		t.Error("a claimed review was handed out twice")
	}
	// A verdict that fails the checks is refused and the review stays open;
	// with a wrong token the checks are not even reached.
	bad := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"ok","criteria":[{"id":"AC-9","status":"met","reason":"x","evidence":["app.go"]}]}`)
	var inv *validation.InvalidVerdictError
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, bad); !errors.As(err, &inv) {
		t.Errorf("invalid verdict: %v", err)
	}
	if err := h.eng.PostReviewVerdict(ctx, b.ID, "wrong-token", bad); !errors.Is(err, change.ErrConflict) {
		t.Errorf("invalid verdict with a wrong token: %v, want ErrConflict", err)
	}
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"The feature does what the intent says.\nOne follow-up is worth doing.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}],"findings":[{"severity":"minor","message":"The error is not named\nin the log line.","file":"a.go","line":4}],"risks":[{"area":"security","message":"The token is read from a flag.","human_required":false}],"usage":{"model":"claude-opus-5-5","input_tokens":1200,"output_tokens":300,"turns":13,"cost_usd":3.06}}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, "wrong-token", good); !errors.Is(err, change.ErrConflict) {
		t.Errorf("wrong token: %v", err)
	}
	// kai's poll was cut off: the claim goes back, otto takes it, kai's token is dead.
	if err := h.eng.ReleaseReview(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * reviewPoll) // let the step see the release
	b2, ok, err := h.eng.ClaimReview(ctx, "", "otto")
	if err != nil || !ok || b2.ID != b.ID || b2.Token == b.Token {
		t.Fatalf("re-claim = %+v %v %v", b2, ok, err)
	}
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); !errors.Is(err, change.ErrConflict) {
		t.Errorf("a released token still worked: %v", err)
	}
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b2.Token, good); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); !errors.Is(err, change.ErrConflict) {
		t.Errorf("second verdict: %v", err)
	}
	done := h.wait(r.ID)
	if done.Outcome != change.Approved {
		t.Fatalf("outcome = %s, result %+v", done.Outcome, done.Result)
	}
	var ext *run.Step
	for i := range done.Steps {
		if done.Steps[i].Name == "ai-review" {
			ext = &done.Steps[i]
		}
	}
	if ext == nil || ext.Status != "succeeded" || !ext.Validator {
		t.Errorf("external step = %+v", ext)
	}
	var found bool
	for _, v := range done.Result.Validators {
		if v.Name == "ai-review" && v.Outcome == "approve" && strings.Contains(v.Summary, "intent says") {
			found = true
		}
	}
	if !found {
		t.Errorf("validators = %+v; want ai-review approve with the posted summary", done.Result.Validators)
	}
	log, _ := os.ReadFile(h.eng.LogPath(r.ID, "ai-review"))
	for _, want := range []string{"waiting for an external reviewer", "claimed by kai", "claim by kai was released", "claimed by otto", "verdict posted by otto",
		"verdict: approve — The feature does what the intent says. (claude-opus-5-5", "1200 input and 300 output tokens", "13 turns", "$3.06",
		"\n  One follow-up is worth doing.\n", "finding minor: The error is not named in the log line. (a.go:4)", "risk security: The token is read from a flag.\n"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("step log lacks %q:\n%s", want, log)
		}
	}
	if rv, err := h.db.GetReview(ctx, b.ID); err != nil || rv.Status != review.Done {
		t.Errorf("review = %+v %v", rv, err)
	}
}

func TestExternalValidatorTimesOutIntoAValidatorError(t *testing.T) {
	h := newHarness(t, Config{})
	fast := strings.Replace(externalYAML, "timeout: 20s", "timeout: 2s", 1)
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": fast}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	if r.Outcome != change.NeedsHuman {
		t.Errorf("outcome = %s, want needs_human after a validator error", r.Outcome)
	}
	for _, s := range r.Steps {
		if s.Name == "ai-review" && s.Status != "timed_out" {
			t.Errorf("external step = %+v, want timed_out", s)
		}
	}
	// The review is closed: a late claim finds nothing.
	if _, ok, _ := h.eng.ClaimReview(ctx, "", "late"); ok {
		t.Error("an expired review was handed out")
	}
	log, _ := os.ReadFile(h.eng.LogPath(r.ID, "ai-review"))
	if !strings.Contains(string(log), "no verdict arrived") {
		t.Errorf("step log lacks the timeout line:\n%s", log)
	}
}

func TestAVerdictPostedJustBeforeTheDeadlineIsTaken(t *testing.T) {
	// Polling every 10s against a 3s step: the only way the step can see
	// the verdict is the last look it takes when its time runs out.
	old := reviewPoll
	reviewPoll = 10 * time.Second
	t.Cleanup(func() { reviewPoll = old })
	h := newHarness(t, Config{})
	slow := strings.Replace(externalYAML, "timeout: 20s", "timeout: 3s", 1)
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": slow}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := h.claimSoon("kai")
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); err != nil {
		t.Fatal(err)
	}
	done := h.wait(r.ID)
	if done.Outcome != change.Approved {
		t.Errorf("outcome = %s; the verdict accepted before the deadline was dropped", done.Outcome)
	}
	for _, s := range done.Steps {
		if s.Name == "ai-review" && s.Status != "succeeded" {
			t.Errorf("external step = %+v", s)
		}
	}
}

func TestACancelledRunKeepsNoLateVerdict(t *testing.T) {
	old := reviewPoll
	reviewPoll = 10 * time.Second
	t.Cleanup(func() { reviewPoll = old })
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalYAML}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	first, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := h.claimSoon("kai")
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); err != nil {
		t.Fatal(err)
	}
	// A new iteration supersedes the first run before its next poll: the
	// accepted verdict belongs to a run that no longer counts.
	second, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := h.wait(first.ID)
	if done.Outcome == change.Approved {
		t.Errorf("the superseded run took the late verdict: %+v", done)
	}
	for _, s := range done.Steps {
		if s.Name == "ai-review" && s.Status == "succeeded" {
			t.Errorf("external step of a cancelled run = %+v", s)
		}
	}
	// The second run publishes its own review and waits for a fresh verdict.
	b2 := h.claimSoon("kai")
	if b2.ID == b.ID || b2.RunID != second.ID {
		t.Errorf("second review = %+v", b2)
	}
}

// openNoReviewerAlerts lists the open no_reviewer alerts.
func (h *harness) openNoReviewerAlerts() []alert.Alert {
	h.t.Helper()
	all, err := h.db.List(ctx, false)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []alert.Alert
	for _, a := range all {
		if a.Kind == alert.NoReviewer {
			out = append(out, a)
		}
	}
	return out
}

func TestReviewerPresenceAndTheNoReviewerAlert(t *testing.T) {
	oldWindow := presenceWindow
	presenceWindow = 2 * time.Second
	t.Cleanup(func() { presenceWindow = oldWindow })
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalYAML}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	if seen := h.eng.Reviewers(); len(seen) != 0 {
		t.Fatalf("a fresh engine has heard from %v", seen)
	}
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Nobody has polled: publishing the review raises the alert, once per repository.
	deadline := time.Now().Add(15 * time.Second)
	for len(h.openNoReviewerAlerts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no no_reviewer alert was raised")
		}
		time.Sleep(100 * time.Millisecond)
	}
	a := h.openNoReviewerAlerts()[0]
	if a.Key != h.repoID || a.RepoID != h.repoID || !strings.Contains(a.Message, "plumbix review --watch") || a.Details["change_id"] != c.ID {
		t.Errorf("alert = %+v", a)
	}
	log, _ := os.ReadFile(h.eng.LogPath(r.ID, "ai-review"))
	if !strings.Contains(string(log), "no reviewer has polled") {
		t.Errorf("step log lacks the hint:\n%s", log)
	}
	// A poll from a reviewer whose filter covers the repository is presence, and resolves the alert.
	if _, _, err := h.eng.ClaimReview(ctx, "r_other", "otto"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.openNoReviewerAlerts()); n != 1 {
		t.Errorf("a poll filtered to another repository resolved the alert (%d open)", n)
	}
	b := h.claimSoon("kai")
	if n := len(h.openNoReviewerAlerts()); n != 0 {
		t.Errorf("kai's poll left %d alert(s) open", n)
	}
	seen := h.eng.Reviewers()
	if len(seen) != 2 || seen[0].Name != "kai" || seen[0].RepoID != "" || time.Since(seen[0].LastSeen) > 5*time.Second || seen[1].Name != "otto" || seen[1].RepoID != "r_other" {
		t.Errorf("reviewers = %+v", seen)
	}
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); err != nil {
		t.Fatal(err)
	}
	if done := h.wait(r.ID); done.Outcome != change.Approved {
		t.Fatalf("outcome = %s", done.Outcome)
	}
	// A review published while kai was seen recently raises nothing; once the
	// window passes with the review still pending, the waiting step raises it.
	r2, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := len(h.openNoReviewerAlerts()); n != 0 {
		t.Errorf("alert raised although kai polled %s ago", time.Since(seen[0].LastSeen))
	}
	deadline = time.Now().Add(20 * time.Second)
	for len(h.openNoReviewerAlerts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the waiting step did not raise the alert after the presence window passed")
		}
		time.Sleep(200 * time.Millisecond)
	}
	b2 := h.claimSoon("kai")
	if err := h.eng.PostReviewVerdict(ctx, b2.ID, b2.Token, good); err != nil {
		t.Fatal(err)
	}
	h.wait(r2.ID)
}

func TestARunningStepIsRecordedWhenItStarts(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalYAML}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	before, _ := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 200)
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// While the external step waits, the run lists it as running, with a log to read.
	b := h.claimSoon("kai")
	time.Sleep(2 * reviewPoll)
	got, err := h.db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ext *run.Step
	for i := range got.Steps {
		if got.Steps[i].Name == "ai-review" {
			ext = &got.Steps[i]
		}
	}
	if ext == nil || ext.Status != "running" || !ext.Validator {
		t.Fatalf("steps while waiting = %+v", got.Steps)
	}
	if log, _ := os.ReadFile(h.eng.LogPath(r.ID, "ai-review")); !strings.Contains(string(log), "claimed by kai") {
		t.Errorf("running step's log = %q", log)
	}
	after, _ := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 200)
	var starts []string
	for _, e := range after[len(before):] {
		if e.Type == events.StepStarted {
			raw, _ := json.Marshal(e.Payload)
			var p events.StepStartedPayload
			_ = json.Unmarshal(raw, &p)
			starts = append(starts, p.Step)
		}
	}
	if strings.Join(starts, ",") != "test,validate,ai-review" && strings.Join(starts, ",") != "test,ai-review,validate" {
		t.Errorf("step.started events = %v", starts)
	}
	// The final record replaces the running one.
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); err != nil {
		t.Fatal(err)
	}
	done := h.wait(r.ID)
	for _, s := range done.Steps {
		if s.Name == "ai-review" && s.Status != "succeeded" {
			t.Errorf("final record = %+v", s)
		}
	}
	if n := len(done.Steps); n != 3 {
		t.Errorf("%d step records, want one per step", n)
	}
}

func TestAReviewerAppendsBoundedProgressLines(t *testing.T) {
	old := ReviewLogMinInterval
	ReviewLogMinInterval = 0
	t.Cleanup(func() { ReviewLogMinInterval = old })
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalYAML}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := h.claimSoon("kai")
	if err := h.eng.AppendReviewLog(ctx, b.ID, "wrong", "turn 1: Read app.go"); !errors.Is(err, change.ErrConflict) {
		t.Errorf("wrong token: %v", err)
	}
	var inv *change.InvalidError
	if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, "   "); !errors.As(err, &inv) {
		t.Errorf("empty line: %v", err)
	}
	if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, strings.Repeat("x", MaxReviewLogLine+1)); !errors.As(err, &inv) {
		t.Errorf("overlong line: %v", err)
	}
	if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, "turn 1: Read app.go\nsecond line"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(h.eng.LogPath(r.ID, "ai-review"))
	if !strings.Contains(string(log), "reviewer: turn 1: Read app.go second line") {
		t.Errorf("log lacks the progress line (newlines folded):\n%s", log)
	}
	// The cap: no more than MaxReviewLogLines per review.
	for i := 1; i < MaxReviewLogLines; i++ {
		if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, fmt.Sprintf("turn %d: Grep x", i)); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
	}
	if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, "one too many"); !errors.Is(err, ErrTooManyLines) {
		t.Errorf("over the cap: %v", err)
	}
	good := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`)
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, good); err != nil {
		t.Fatal(err)
	}
	h.wait(r.ID)
	// After the verdict the review is done: nothing more is appended, and
	// its counters are gone.
	if err := h.eng.AppendReviewLog(ctx, b.ID, b.Token, "late"); !errors.Is(err, change.ErrConflict) {
		t.Errorf("append after the verdict: %v", err)
	}
	h.eng.reviewLogMu.Lock()
	n := len(h.eng.reviewLogs)
	h.eng.reviewLogMu.Unlock()
	if n != 0 {
		t.Errorf("%d review log counters kept after the step ended, want none", n)
	}
}

// TestAnExternalStepTakesToolsAndModelFromItsProfile (WU-028): the brief
// carries the named anthropic profile's tool policy, model and name; an
// openai profile named on an external step fails it, since the loop runs
// Claude Code.
func TestAnExternalStepTakesToolsAndModelFromItsProfile(t *testing.T) {
	settingsFake := fakeSettings{reviewers: map[string]settings.Reviewer{"claude": claudeProfile, "grok": grokProfile}, def: "grok"}
	h := newHarness(t, Config{Settings: settingsFake})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": strings.Replace(externalYAML, "external: true,", "external: true, reviewer: claude,", 1)}, "external review")
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := h.claimSoon("kai")
	if b.Tools != review.ToolsFull || b.Model != "claude-sonnet-5-5" || b.Reviewer != "claude" {
		t.Errorf("brief tools %q model %q reviewer %q, want the claude profile's", b.Tools, b.Model, b.Reviewer)
	}
	if err := h.eng.PostReviewVerdict(ctx, b.ID, b.Token, []byte(approvingVerdict)); err != nil {
		t.Fatal(err)
	}
	h.wait(r.ID)

	// No reviewer named and an openai default: the built-in values apply.
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": externalYAML, "b.go": "package app\n"}, "no profile named")
	r2, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b2 := h.claimSoon("kai")
	if b2.Tools != review.ToolsReadOnly || b2.Model != "" || b2.Reviewer != "" {
		t.Errorf("brief with an openai default: tools %q model %q reviewer %q, want the built-in values", b2.Tools, b2.Model, b2.Reviewer)
	}
	if err := h.eng.PostReviewVerdict(ctx, b2.ID, b2.Token, []byte(approvingVerdict)); err != nil {
		t.Fatal(err)
	}
	h.wait(r2.ID)

	// An openai profile named on the external step: the step errors and says why.
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": strings.Replace(externalYAML, "external: true,", "external: true, reviewer: grok,", 1), "c.go": "package app\n"}, "wrong kind")
	r3 := h.validate(c.ID)
	h.wait(r3.ID)
	done, _ := h.db.GetRun(ctx, r3.ID)
	log, _ := os.ReadFile(h.eng.LogPath(r3.ID, "ai-review"))
	if done.Outcome == change.Approved || !strings.Contains(string(log), `reviewer profile "grok" asks an OpenAI-compatible endpoint`) {
		t.Errorf("external step with an openai profile: outcome %s, log:\n%s", done.Outcome, log)
	}
}
