package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

const disabledYAML = "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n  - {name: b, needs: [a], disabled: {reason: task, note: PublishTestResults@2}}\n  - {name: c, image: x, run: x, disabled: {reason: task}}\n"

// TestPipelineEndpointReportsDisabledSteps (WU-044).
func TestPipelineEndpointReportsDisabledSteps(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("imported", map[string]string{".plumbix/pipeline.yaml": disabledYAML}, "import")
	rec := a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline?branch=imported", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	o.check("PipelineFile", rec)
	got := decode[pipelineFile](t, rec)
	if got.Pipeline == nil || !got.Pipeline.Disabled {
		t.Fatalf("pipeline = %+v, want disabled", got.Pipeline)
	}
	if d := got.Pipeline.Steps[1].Disabled; d == nil || d.Reason != "task" || d.Note != "PublishTestResults@2" {
		t.Errorf("disabled = %+v", d)
	}
	if got.Pipeline.Steps[0].Disabled != nil {
		t.Error("an enabled step is served as disabled")
	}

	rec = a.do(http.MethodGet, "/api/v1/repos/"+a.repoID+"/pipeline", "")
	if enabled := decode[pipelineFile](t, rec); enabled.Pipeline == nil || enabled.Pipeline.Disabled {
		t.Errorf("main pipeline = %+v, want enabled", enabled.Pipeline)
	}
}

func TestCheckPipelineReportsBlockers(t *testing.T) {
	o := loadOpenAPI(t)
	rec := postCheck(t, disabledYAML)
	o.check("PipelineCheck", rec)
	var resp struct {
		Valid    bool           `json:"valid"`
		Disabled bool           `json:"disabled"`
		Blockers map[string]int `json:"blockers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Valid || !resp.Disabled || !reflect.DeepEqual(resp.Blockers, map[string]int{"task": 2}) {
		t.Errorf("response = %+v", resp)
	}
	var enabled map[string]any
	_ = json.Unmarshal(postCheck(t, "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n").Body.Bytes(), &enabled)
	if enabled["disabled"] != false || enabled["blockers"] != nil {
		t.Errorf("enabled = %v, want disabled false and no blockers", enabled)
	}
}
