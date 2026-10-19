package engine

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

const disabledPipeline = `version: 1
steps:
  - {name: test, image: "golang:1.27", run: "go test ./..."}
  - {name: integration, needs: [test], disabled: {reason: services, note: needs postgres}}
  - {name: validate, image: "plumbix-reference-validator:dev", validator: true, needs: [test, integration]}
`

// TestDisabledPipelineNeverRuns (WU-044): a change whose pipeline holds a
// disabled step errors before any step runs and waits for a person.
func TestDisabledPipelineNeverRuns(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": disabledPipeline}, "import a step")
	c := h.submit("feat")
	r := h.validate(c.ID)
	if r.Status != run.Errored || r.Outcome != change.NeedsHuman || h.state(c.ID) != change.NeedsHuman {
		t.Fatalf("run = %+v", r)
	}
	if !strings.Contains(r.Error, "disabled") || !strings.Contains(r.Error, "services (1)") {
		t.Errorf("error = %q, want the blocking reasons", r.Error)
	}
	if len(h.fake.jobs) != 0 {
		t.Errorf("%d steps ran in a disabled pipeline", len(h.fake.jobs))
	}
}

func TestDisabledPipelineBranchRunErrors(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("ci-only", map[string]string{".plumbix/pipeline.yaml": disabledPipeline}, "import a step")
	r, err := h.eng.RunBranch(ctx, h.repoID, "ci-only")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(r.ID)
	if got.Status != run.Errored || !strings.Contains(got.Error, "services (1)") || len(h.fake.jobs) != 0 {
		t.Fatalf("run = %+v, jobs %d", got, len(h.fake.jobs))
	}
}
