package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/run"
)

var apiTriggered = map[string]string{
	".plumbix/pipelines/deploy.yaml":   "version: 1\non: {push: {branches: [main], paths: [\"svc/**\"]}}\nsteps:\n  - {name: deploy-svc, image: \"deploy-img:1\", run: x}\n",
	".plumbix/pipelines/off.yaml":      "version: 1\non: {manual: true}\ndisabled: {reason: trigger, note: \"tags: v*\"}\nsteps:\n  - {name: off-step, image: x, run: x}\n",
	".plumbix/pipelines/bad.yaml":      "version: 1\nsteps:\n  - {name: bad-step, image: x, run: x}\n",
	".plumbix/pipelines/handonly.yaml": "version: 1\non: {manual: true}\nsteps:\n  - {name: hand-step, image: x, run: x}\n",
}

// TestTriggeredPipelinesEndpoints (WU-045): list, run by hand, refusals.
func TestTriggeredPipelinesEndpoints(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("main", apiTriggered, "add pipelines")

	rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipelines", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	o.check("TriggeredPipelines", rec)
	var list struct {
		Branch    string `json:"branch"`
		Pipelines []struct {
			Name     string           `json:"name"`
			File     string           `json:"file"`
			Disabled bool             `json:"disabled"`
			Blockers map[string]int   `json:"blockers"`
			Problems []map[string]any `json:"problems"`
			On       *struct {
				Push *struct {
					Branches []string `json:"branches"`
				} `json:"push"`
				Manual bool `json:"manual"`
			} `json:"on"`
		} `json:"pipelines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Branch != "main" || len(list.Pipelines) != 4 {
		t.Fatalf("list = %+v", list)
	}
	by := map[string]int{}
	for i, p := range list.Pipelines {
		by[p.Name] = i
	}
	if p := list.Pipelines[by["deploy"]]; p.On == nil || p.On.Push == nil || p.On.Push.Branches[0] != "main" || p.Disabled || p.File != ".plumbix/pipelines/deploy.yaml" {
		t.Errorf("deploy = %+v", p)
	}
	if p := list.Pipelines[by["off"]]; !p.Disabled || p.Blockers["trigger"] != 1 {
		t.Errorf("off = %+v", p)
	}
	if p := list.Pipelines[by["bad"]]; p.On != nil || len(p.Problems) == 0 {
		t.Errorf("bad = %+v", p)
	}

	rec = a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipelines/handonly/runs", `{"branch": "main"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Run", rec)
	r := a.waitRun(decode[run.Run](t, rec).ID)
	if r.Status != run.Finished || r.Pipeline != "handonly" {
		t.Errorf("run = %+v", r)
	}
	// A triggered run is not the editor's last run of the change pipeline.
	if f := decode[pipelineFile](t, a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline", "")); f.LastRun != nil {
		t.Errorf("last_run = %+v, want none", f.LastRun)
	}

	for name, want := range map[string]struct {
		status int
		code   string
	}{
		"deploy": {http.StatusConflict, "not_manual"},
		"off":    {http.StatusConflict, "pipeline_disabled"},
		"bad":    {http.StatusConflict, "pipeline_invalid"},
		"nope":   {http.StatusNotFound, "not_found"},
		// handonly exists, but not on this branch's remote.
		"handonly@missing": {http.StatusNotFound, "not_found"},
	} {
		pipe, req := name, `{}`
		if n, b, ok := strings.Cut(name, "@"); ok {
			pipe, req = n, `{"branch": "`+b+`"}`
		}
		rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipelines/"+pipe+"/runs", req)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != want.status || body.Error.Code != want.code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, rec.Body.String(), want.status, want.code)
		}
	}
}

func TestImagesCountTriggeredPipelines(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("main", apiTriggered, "add pipelines")
	rec := a.do(http.MethodGet, "/api/v1/images", "")
	o.check("Images", rec)
	var view struct {
		Images []struct {
			Ref    string `json:"ref"`
			UsedBy []struct {
				Step     string `json:"step"`
				Pipeline string `json:"pipeline"`
			} `json:"used_by"`
		} `json:"images"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	for _, im := range view.Images {
		if im.Ref == "deploy-img:1" {
			if len(im.UsedBy) != 1 || im.UsedBy[0].Step != "deploy-svc" || im.UsedBy[0].Pipeline != "deploy" {
				t.Errorf("used_by = %+v", im.UsedBy)
			}
			return
		}
	}
	t.Errorf("deploy-img:1 missing from %+v", view.Images)
}
