package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// TestImportPreviewEndpoint (WU-046).
func TestImportPreviewEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("main", map[string]string{
		"azure-pipelines.yml":        "trigger: [main]\njobs:\n  - job: Test\n    container: node:20\n    steps:\n      - script: npm test\n  - job: Publish\n    dependsOn: Test\n    container: node:20\n    steps:\n      - task: PublishTestResults@2\n",
		"azure-pipelines-broken.yml": "trigger: [main]\nsteps: [\n",
	}, "azure")
	rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/import?source=azure", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	o.check("ImportPreview", rec)
	var v struct {
		Branch    string `json:"branch"`
		Source    string `json:"source"`
		Pipelines []struct {
			Name     string         `json:"name"`
			File     string         `json:"file"`
			Disabled bool           `json:"disabled"`
			Blockers map[string]int `json:"blockers"`
			Steps    []struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
				File   string `json:"file"`
				Line   int    `json:"line"`
			} `json:"steps"`
		} `json:"pipelines"`
		Problems []struct {
			SourceFile string `json:"source_file"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Branch != "main" || v.Source != "azure" || len(v.Pipelines) != 1 {
		t.Fatalf("view = %+v", v)
	}
	p := v.Pipelines[0]
	if p.Name != "azure-pipelines" || !p.Disabled || p.Blockers["task"] != 1 || len(p.Steps) != 2 || p.Steps[1].Reason != "task" || p.Steps[0].Line == 0 || p.Steps[0].File != "azure-pipelines.yml" {
		t.Errorf("pipeline = %+v", p)
	}
	if len(v.Problems) != 1 || v.Problems[0].SourceFile != "azure-pipelines-broken.yml" {
		t.Errorf("problems = %+v", v.Problems)
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/import?source=jenkins", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown source: %d", rec.Code)
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/import?source=azure&branch=missing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown branch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/r_nope/import?source=azure", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repo: %d", rec.Code)
	}
}

// TestImportChangeEndpoint (WU-048): the import is committed on a new
// branch and opened as a change that needs a person; the preview shows
// the plan; with the files landed, a re-check is up to date.
func TestImportChangeEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("main", map[string]string{
		"azure-pipelines.yml":        "trigger: [main]\njobs:\n  - job: Test\n    container: node:20\n    steps:\n      - script: npm test\n  - job: Publish\n    dependsOn: Test\n    container: node:20\n    steps:\n      - task: PublishTestResults@2\n",
		"azure-pipelines-broken.yml": "trigger: [main]\nsteps: [\n",
	}, "azure")
	prev := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/import?source=azure", "")
	o.check("ImportPreview", prev)
	var v struct {
		Files []struct {
			Path, Status, Content string
			SourceFile            string `json:"source_file"`
		} `json:"files"`
	}
	if err := json.Unmarshal(prev.Body.Bytes(), &v); err != nil || len(v.Files) != 1 || v.Files[0].Status != "added" || v.Files[0].Path != ".plumbix/pipelines/azure-pipelines.yaml" || v.Files[0].SourceFile != "azure-pipelines.yml" || v.Files[0].Content == "" {
		t.Fatalf("preview files = %+v %v", v.Files, err)
	}

	rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/import/changes", `{"source": "azure"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	o.check("ImportChange", rec)
	out := decode[struct {
		Change change.Change
		Files  []struct{ Path, Status string }
	}](t, rec)
	c := out.Change
	if !strings.HasPrefix(c.SourceBranch, "plumbix/import-") || c.TargetBranch != "main" || c.Title != "Import pipelines from Azure Pipelines" || c.SubmittedBy != "pipeline import" || !c.PlumbixBranch || len(out.Files) != 1 {
		t.Errorf("change = %+v files %+v", c, out.Files)
	}
	for _, want := range []string{".plumbix/pipelines/azure-pipelines.yaml (added, from azure-pipelines.yml)", "disabled: task 1", "azure-pipelines-broken.yml"} {
		if !strings.Contains(c.Intent, want) {
			t.Errorf("intent lacks %q:\n%s", want, c.Intent)
		}
	}
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	if r := a.waitRun(first.ID); r.Outcome != change.NeedsHuman {
		t.Errorf("outcome = %s, want needs_human", r.Outcome)
	}
	again := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/import/changes", `{"source": "azure", "branch": "`+c.SourceBranch+`"}`)
	if again.Code != http.StatusConflict || decode[apiError](t, again).Error.Code != "branch_exists" {
		t.Errorf("same branch: %d %s", again.Code, again.Body.String())
	}

	a.repo.Commit("main", map[string]string{v.Files[0].Path: v.Files[0].Content}, "the import lands")
	up := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/import/changes", `{"source": "azure", "branch": "plumbix/recheck"}`)
	o.check("Error", up)
	if up.Code != http.StatusConflict || decode[apiError](t, up).Error.Code != "up_to_date" {
		t.Errorf("re-check: %d %s", up.Code, up.Body.String())
	}
	for body, want := range map[string]int{
		`{"source": "jenkins"}`:                     http.StatusBadRequest,
		`{"source": "azure", "branch": "a..b"}`:     http.StatusBadRequest,
		`{"source": "azure", "target_branch": "x"}`: http.StatusNotFound,
		`{"source": "azure", "title": ""}`:          http.StatusConflict,
	} {
		if rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/import/changes", body); rec.Code != want {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}
