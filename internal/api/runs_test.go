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
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/store"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// approvingRunner passes every step and makes validators approve.
type approvingRunner struct{}

func (approvingRunner) Run(_ context.Context, job runner.Job) (runner.Result, error) {
	_, _ = io.WriteString(job.Log, "hello from "+job.Name+"\n")
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
}

func newRunAPI(t *testing.T) *runAPI {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(filepath.Join(data, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	eng := engine.New(db, approvingRunner{}, engine.Config{DataDir: data, MaxParallel: 2, DefaultTimeout: time.Minute, MaxFixRounds: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { eng.Shutdown(context.Background()) })
	repo := gittest.New(t)
	repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test]}\n"}, "pipeline")
	repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	repoID := "r_apitestrepo1"
	if err := db.CreateRepo(context.Background(), change.Repo{ID: repoID, Host: "github", URL: repo.Remote, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Deps{Version: "dev", Token: testToken, Changes: change.NewService(db), Engine: eng, Runs: db})
	return &runAPI{testAPI: &testAPI{t: t, h: h}, repoID: repoID, db: db}
}

func (a *runAPI) waitRun(id string) run.Run {
	a.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		r := decode[run.Run](a.t, a.do(http.MethodGet, "/api/v1/runs/"+id, ""))
		if r.Status != run.Running {
			return r
		}
	}
	a.t.Fatal("run did not finish")
	return run.Run{}
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
