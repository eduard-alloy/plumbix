package api

import (
	"encoding/json"
	"net/http"
	"testing"
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
