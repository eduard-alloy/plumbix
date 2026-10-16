package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/refvalidator"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
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
	if err := db.CreateRepo(ctx, change.Repo{ID: h.repoID, Host: "github", URL: h.repo.Remote, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) submit(branch string) change.Change {
	h.t.Helper()
	c, err := h.svc.Submit(ctx, change.ChangeInput{
		RepoID: h.repoID, SourceBranch: branch, TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}}},
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

func (h *harness) wait(runID string) run.Run {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r, err := h.db.GetRun(ctx, runID)
		if err != nil {
			h.t.Fatal(err)
		}
		if r.Status != run.Running {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("run %s did not finish", runID)
	return run.Run{}
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
