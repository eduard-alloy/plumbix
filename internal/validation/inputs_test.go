package validation

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

func sampleInput() Input {
	line := 12
	c := change.Change{
		ID: "c_abcdefghijkl", Title: "Add retries", Intent: "Retry flaky uploads",
		SourceBranch: "feat", TargetBranch: "main",
		AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "retries", Check: &change.Check{Steps: []string{"test"}}}},
		Constraints:        []string{"no new deps"},
	}
	return NewInput(c, "1111111", "2222222", []string{"upload.go", "upload_test.go"}, 2,
		[]Finding{{Severity: "major", Message: "add a test", File: "upload.go", Line: &line}})
}

func sampleEvidence() []StepEvidence {
	return []StepEvidence{
		{Step: "test", Status: "succeeded", ExitCode: 0, DurationMS: 1500, Log: []byte("ok\n")},
		{Step: "lint", Status: "failed", ExitCode: 1, DurationMS: 300, Log: []byte("bad\n")},
	}
}

func TestPrepareInputsLayoutAndRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "in")
	in := sampleInput()
	if err := PrepareInputs(dir, in, sampleEvidence()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"change.json", "evidence/test/status.json", "evidence/test/log.txt", "evidence/lint/status.json", "evidence/lint/log.txt"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	got, err := ReadInput(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, in)
	}
	st, ok, err := ReadStepStatus(dir, "lint")
	if err != nil || !ok || st.Status != "failed" || st.ExitCode != 1 || st.DurationMS != 300 || st.Plumbix != StepStatusVersion {
		t.Errorf("lint status = %+v, %v, %v", st, ok, err)
	}
	if _, ok, err := ReadStepStatus(dir, "missing"); ok || err != nil {
		t.Errorf("missing step: ok=%v err=%v", ok, err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "evidence", "test", "log.txt"))
	if string(log) != "ok\n" {
		t.Errorf("log = %q", log)
	}
}

func TestPrepareInputsRejectsUnsafeStepNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", "", "."} {
		err := PrepareInputs(t.TempDir(), sampleInput(), []StepEvidence{{Step: name, Status: "succeeded"}})
		if err == nil {
			t.Errorf("step name %q accepted", name)
		}
	}
}

func TestInputFilesMatchSchema(t *testing.T) {
	dir := t.TempDir()
	if err := PrepareInputs(dir, sampleInput(), sampleEvidence()); err != nil {
		t.Fatal(err)
	}
	schema := compileSchema(t, "validator-input.schema.json")
	data, _ := os.ReadFile(filepath.Join(dir, "change.json"))
	if err := validateJSON(t, schema, data); err != nil {
		t.Errorf("change.json does not match the schema: %v", err)
	}
	status := compileSchemaRef(t, "validator-input.schema.json", "#/$defs/stepStatus")
	for _, step := range []string{"test", "lint"} {
		data, _ := os.ReadFile(filepath.Join(dir, "evidence", step, "status.json"))
		if err := validateJSON(t, status, data); err != nil {
			t.Errorf("%s/status.json does not match the schema: %v", step, err)
		}
	}
}
