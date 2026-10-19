package engine

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/importer/azure"
)

// TestImportPreview (WU-046): the Azure pipelines at a branch head are
// converted; other files are ignored, huge ones reported; nothing is written.
func TestImportPreview(t *testing.T) {
	h := newHarness(t, Config{})
	head := h.repo.Commit("main", map[string]string{
		"pipelines/azure-pipelines-ci.yml": "trigger: [main]\ncontainer: node:20\nsteps:\n  - script: npm test\n",
		"config/settings.yml":              "name: x\n",
		"pipelines/huge.yml":               "trigger: none\nsteps:\n  - script: x\n# " + strings.Repeat("x", 1<<20) + "\n",
	}, "azure pipelines")
	v, err := h.eng.ImportPreview(ctx, h.repoID, "main", azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if v.SHA != head || v.Branch != "main" || len(v.Result.Pipelines) != 1 {
		t.Fatalf("view = %+v", v)
	}
	p := v.Result.Pipelines[0]
	if p.Name != "ci" || p.File != ".plumbix/pipelines/ci.yaml" || p.SourceFile != "pipelines/azure-pipelines-ci.yml" || !strings.Contains(string(p.Content), "npm test") {
		t.Errorf("pipeline = %+v", p)
	}
	if len(v.Result.Problems) != 1 || v.Result.Problems[0].SourceFile != "pipelines/huge.yml" {
		t.Errorf("problems = %+v", v.Result.Problems)
	}
	if files, _ := h.eng.TriggeredPipelines(ctx, h.repoID, "main"); len(files.Pipelines) != 0 {
		t.Error("the preview wrote pipelines")
	}
}
