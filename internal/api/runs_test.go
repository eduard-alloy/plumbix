package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/images"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
	"github.com/eduard-alloy/plumbix/internal/store"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var ctxBG = context.Background()

// approvingRunner passes every step and makes validators approve.
type approvingRunner struct{}

func (approvingRunner) Run(_ context.Context, job runner.Job) (runner.Result, error) {
	_, _ = io.WriteString(job.Log, "hello from "+job.Name+"\n")
	if job.Script == "fail" {
		return runner.Result{ExitCode: 1}, nil
	}
	for _, m := range job.Mounts {
		if m.Target != validation.OutPath {
			continue
		}
		v := validation.Verdict{Plumbix: validation.VerdictVersion, Outcome: validation.Approve, Summary: "fine",
			Criteria: []validation.CriterionVerdict{{ID: "AC-1", Status: validation.Met, Reason: "ok", Evidence: []string{"evidence/test/log.txt"}}}}
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(m.Source, validation.VerdictFile), data, 0o644); err != nil {
			return runner.Result{}, err
		}
	}
	return runner.Result{}, nil
}

type runAPI struct {
	*testAPI
	repoID string
	db     *store.DB
	repo   *gittest.Repo
	hub    *events.Hub
	docker *fakeDockerImages
}

func newRunAPI(t *testing.T) *runAPI {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(filepath.Join(data, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sec, err := secrets.Open(filepath.Join(data, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(db, approvingRunner{}, engine.Config{DataDir: data, MaxParallel: 2, DefaultTimeout: time.Minute, MaxFixRounds: 3, Hosts: host.Live(sec.Values), Settings: settings.New(db)}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { eng.Shutdown(context.Background()) })
	repo := gittest.New(t)
	repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test]}\n"}, "pipeline")
	repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	repoID := "r_apitestrepo1"
	if err := db.CreateRepo(context.Background(), change.Repo{ID: repoID, Host: "github", URL: repo.Remote, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(db)
	db.OnEvent(hub.Publish)
	t.Cleanup(hub.Close)
	docker := &fakeDockerImages{known: map[string]bool{}}
	h := NewHandler(Deps{Version: "dev", Token: testToken, Changes: change.NewService(db), Engine: eng, Runs: db, Alerts: db, Events: hub, Settings: settings.New(db), Secrets: sec, Hosts: host.Live(sec.Values),
		Images: images.New(docker, "dev", filepath.Join(t.TempDir(), "builds"))})
	return &runAPI{testAPI: &testAPI{t: t, h: h}, repoID: repoID, db: db, repo: repo, hub: hub, docker: docker}
}

func TestRunStepsCarryRetriesAllowancesReasonsAndStages(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("rich", map[string]string{".plumbix/pipeline.yaml": `version: 1
steps:
  - {name: test, image: x, run: x}
  - {name: flaky, image: x, run: fail, retries: 1, allow_failure: true}
  - {name: api, image: x, run: x, when: {paths: ["api/**"]}}
  - {name: after, image: x, run: x, needs: [flaky, api]}
  - {name: validate, image: v, validator: true, needs: [test, after]}
stages:
  checks: [flaky, api, after]
`}, "rich pipeline")
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "rich")))
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	a.waitRun(first.ID)
	rec := a.do(http.MethodGet, "/api/v1/runs/"+first.ID, "")
	o.check("Run", rec)
	body := rec.Body.String()
	for _, want := range []string{`"attempts":2`, `"allowed_failure":true`, `"reason":"paths"`, `"stage":"checks"`} {
		if !strings.Contains(body, want) {
			t.Errorf("run JSON lacks %s:\n%s", want, body)
		}
	}
	by := map[string]run.Step{}
	for _, s := range decode[run.Run](t, rec).Steps {
		by[s.Name] = s
	}
	if s := by["flaky"]; s.Status != "failed" || s.Attempts != 2 || !s.AllowedFailure || s.Stage != "checks" {
		t.Errorf("flaky = %+v", s)
	}
	if s := by["api"]; s.Status != "skipped" || s.Reason != "paths" {
		t.Errorf("api = %+v", s)
	}
	if s := by["after"]; s.Status != "succeeded" {
		t.Errorf("after = %+v", s)
	}
	if s := by["test"]; s.Attempts != 0 || s.Stage != "" || s.Reason != "" || s.AllowedFailure {
		t.Errorf("test carries fields it should omit: %+v", s)
	}
}

// waitRun returns the run once it ended and its change, if any, has left
// running: the engine records the run first and moves the change right
// after, and a test acting in that gap races the engine.
func (a *runAPI) waitRun(id string) run.Run {
	a.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var r run.Run
	for {
		if r = decode[run.Run](a.t, a.do(http.MethodGet, "/api/v1/runs/"+id, "")); r.Status != run.Running {
			break
		}
		if !time.Now().Before(deadline) {
			a.t.Fatal("run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.ChangeID == "" || r.Status == run.Cancelled {
		return r
	}
	for time.Now().Before(deadline) {
		if c := decode[change.Change](a.t, a.do(http.MethodGet, "/api/v1/changes/"+r.ChangeID, "")); c.State != change.Running {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatalf("change %s still running after run %s ended", r.ChangeID, id)
	return r
}

func TestSubmitStartsValidation(t *testing.T) {
	a := newRunAPI(t)
	rec := a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	c := decode[change.Change](t, rec)
	if c.State != change.Running {
		t.Errorf("state after submit = %s, want running", c.State)
	}
	runs := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", ""))
	if len(runs.Runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	r := a.waitRun(runs.Runs[0].ID)
	if r.Outcome != change.Approved || len(r.Steps) != 2 {
		t.Fatalf("run = %+v", r)
	}
	if got := decode[change.Change](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID, "")); got.State != change.Approved {
		t.Errorf("change state = %s", got.State)
	}
	logRec := a.do(http.MethodGet, "/api/v1/runs/"+r.ID+"/steps/test/log", "")
	if logRec.Code != http.StatusOK || !strings.Contains(logRec.Body.String(), "hello from test") || !strings.HasPrefix(logRec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("log: %d %q %s", logRec.Code, logRec.Body.String(), logRec.Header().Get("Content-Type"))
	}
}

func TestRevalidateAndErrors(t *testing.T) {
	a := newRunAPI(t)
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat")))
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	a.waitRun(first.ID)
	rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/validate", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body.String())
	}
	second := decode[run.Run](t, rec)
	if second.Iteration != 2 {
		t.Errorf("iteration = %d", second.Iteration)
	}
	a.waitRun(second.ID)
	_ = a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/close", "")
	if rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/validate", ""); rec.Code != http.StatusConflict {
		t.Errorf("validate closed change: %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/changes/c_nope/runs", "/api/v1/runs/run_nope", "/api/v1/runs/" + second.ID + "/steps/nope/log", "/api/v1/runs/" + second.ID + "/steps/..%2f..%2fetc/log"} {
		if rec := a.do(http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, rec.Code)
		}
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/c_nope/validate", ""); rec.Code != http.StatusNotFound {
		t.Errorf("validate unknown change: %d", rec.Code)
	}
}

func TestRunResponsesMatchOpenAPI(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat")))
	listRec := a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")
	o.check("RunList", listRec)
	first := decode[struct{ Runs []run.Run }](t, listRec).Runs[0]
	a.waitRun(first.ID)
	o.check("Run", a.do(http.MethodGet, "/api/v1/runs/"+first.ID, ""))
	o.check("RunList", a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", ""))
	started := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/validate", "")
	o.check("Run", started)
	a.waitRun(decode[run.Run](t, started).ID)
	o.check("Error", a.do(http.MethodGet, "/api/v1/runs/run_aaaaaaaaaaaa", ""))
}

func TestRepoRuns(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat")))
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	a.waitRun(first.ID)
	rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/runs?branch=feat&limit=5", "")
	o.check("RunList", rec)
	runs := decode[struct{ Runs []run.Run }](t, rec).Runs
	if len(runs) != 1 || runs[0].Branch != "feat" {
		t.Errorf("runs = %+v", runs)
	}
	if got := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/runs?branch=other", "")).Runs; len(got) != 0 {
		t.Errorf("other branch = %+v", got)
	}
	for path, status := range map[string]int{
		"/api/v1/repos/r_nope/runs":                     http.StatusNotFound,
		"/api/v1/repos/" + a.repoID + "/runs?limit=0":   http.StatusBadRequest,
		"/api/v1/repos/" + a.repoID + "/runs?limit=999": http.StatusBadRequest,
	} {
		if rec := a.do(http.MethodGet, path, ""); rec.Code != status {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, status)
		}
	}
}
