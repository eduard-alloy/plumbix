package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

func TestRepoPipelineEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	o.check("PipelineFile", rec)
	got := decode[pipelineFile](t, rec)
	if got.File != ".plumbix/pipeline.yaml" || got.Branch != "main" || got.SHA == "" || got.Pipeline == nil || len(got.Pipeline.Steps) != 2 || got.LastRun != nil {
		t.Errorf("view = %+v", got)
	}
	if st := got.Pipeline.Steps[0]; st.Name != "test" || len(st.Commands) != 1 || st.Commands[0] != "x" || st.Needs == nil || st.Paths == nil || st.Env == nil {
		t.Errorf("step = %+v", st)
	}
	if !strings.Contains(got.Content, "validator: true") {
		t.Errorf("content = %q", got.Content)
	}

	// After a run on the branch, last_run carries the step durations.
	c := a.approved("feat")
	_ = c
	rec = a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=feat", "")
	o.check("PipelineFile", rec)
	withRun := decode[pipelineFile](t, rec)
	if withRun.LastRun == nil || withRun.LastRun.Steps["test"].Status != "succeeded" {
		t.Errorf("last_run = %+v", withRun.LastRun)
	}

	a.repo.Commit("broken", map[string]string{".plumbix/pipeline.yaml": "version: 2\n"}, "break")
	rec = a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=broken", "")
	o.check("PipelineFile", rec)
	broken := decode[pipelineFile](t, rec)
	if broken.Pipeline != nil || len(broken.Problems) == 0 || broken.Content != "version: 2\n" {
		t.Errorf("broken = %+v", broken)
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown branch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/r_nope/pipeline", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repo: %d", rec.Code)
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=..", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad branch: %d", rec.Code)
	}
}

func TestProposePipelineOpensAChange(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	content := "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: lint, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test, lint]}\n"
	body := `{"branch": "pipeline/add-lint", "title": "Pipeline: add lint", "intent": "Lint every change.", "content": ` + jsonString(content) + `}`
	rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Change", rec)
	c := decode[change.Change](t, rec)
	if c.SourceBranch != "pipeline/add-lint" || c.TargetBranch != "main" || c.Title != "Pipeline: add lint" || len(c.AcceptanceCriteria) != 2 || c.SubmittedBy != "pipeline editor" || !c.PlumbixBranch {
		t.Errorf("change = %+v", c)
	}
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	if r := a.waitRun(first.ID); r.Outcome != change.NeedsHuman {
		t.Errorf("outcome = %s, want needs_human for a pipeline edit", r.Outcome)
	}
	view := decode[pipelineFile](t, a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=pipeline/add-lint", ""))
	if view.Content != content || len(view.Pipeline.Steps) != 3 {
		t.Errorf("file on the new branch = %+v", view)
	}

	again := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", body)
	o.check("Error", again)
	if again.Code != http.StatusConflict || decode[apiError](t, again).Error.Code != "branch_exists" {
		t.Errorf("same branch twice: %d %s", again.Code, again.Body.String())
	}
	bad := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", `{"branch": "pipeline/bad", "title": "t", "content": "version: 2\n"}`)
	o.check("Error", bad)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "content.version") {
		t.Errorf("invalid content: %d %s", bad.Code, bad.Body.String())
	}
	missing := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", `{"branch": "", "title": "", "content": ""}`)
	if missing.Code != http.StatusBadRequest || strings.Count(missing.Body.String(), `"field"`) != 3 {
		t.Errorf("missing fields: %d %s", missing.Code, missing.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", `{"branch": "pipeline/x", "title": "t", "target_branch": "nope", "content": `+jsonString(content)+`}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown target: %d %s", rec.Code, rec.Body.String())
	}
	if main := a.remoteMain(); main == "" {
		t.Error("main vanished")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
