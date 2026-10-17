package aivalidator

// Test fixture shared with the review package's tests: the prompt builder
// moved there (WU-023), the client tests still need the same inputs.

import (
	"path/filepath"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var testCriteria = []change.Criterion{
	{ID: "AC-1", Text: "Uploads retry three times", Check: &change.Check{Steps: []string{"test"}}},
	{ID: "AC-2", Text: "README documents retries"},
}

type fixture struct{ in, ws string }

func newFixture(t *testing.T, changed []string, evidence []validation.StepEvidence) fixture {
	t.Helper()
	f := fixture{in: filepath.Join(t.TempDir(), "in"), ws: t.TempDir()}
	c := change.Change{ID: "c_aaaaaaaaaaaa", Title: "Add retries", Intent: "Make uploads survive flaky networks", SourceBranch: "feat", TargetBranch: "main",
		AcceptanceCriteria: testCriteria, Constraints: []string{"no new dependencies"}}
	line := 3
	prev := []validation.Finding{{Severity: "major", Message: "missing backoff", File: "upload.go", Line: &line}}
	if err := validation.PrepareInputs(f.in, validation.NewInput(c, "base1", "head2", changed, 2, prev), evidence); err != nil {
		t.Fatal(err)
	}
	return f
}
