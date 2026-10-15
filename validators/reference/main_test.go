package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

func TestWritesVerdict(t *testing.T) {
	in, out, ws := filepath.Join(t.TempDir(), "in"), t.TempDir(), t.TempDir()
	crit := []change.Criterion{{ID: "AC-1", Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}}}
	c := change.Change{ID: "c_aaaaaaaaaaaa", Title: "t", Intent: "i", SourceBranch: "f", TargetBranch: "main", AcceptanceCriteria: crit}
	if err := validation.PrepareInputs(in, validation.NewInput(c, "b", "h", nil, 1, nil), []validation.StepEvidence{{Step: "test", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws, "PLUMBIX_CHECK_TIMEOUT": "5s"}
	var stderr bytes.Buffer
	if code := run(func(k string) string { return env[k] }, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	v, err := validation.ReadVerdict(filepath.Join(out, validation.VerdictFile), crit)
	if err != nil || v.Outcome != validation.Approve {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
}

func TestBadTimeoutIsError(t *testing.T) {
	env := map[string]string{"PLUMBIX_CHECK_TIMEOUT": "soon"}
	if code := run(func(k string) string { return env[k] }, &bytes.Buffer{}); code == 0 {
		t.Fatal("bad timeout accepted")
	}
}

func TestMissingInputLeavesNoVerdict(t *testing.T) {
	out := t.TempDir()
	env := map[string]string{"PLUMBIX_IN": t.TempDir(), "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": t.TempDir()}
	if code := run(func(k string) string { return env[k] }, &bytes.Buffer{}); code == 0 {
		t.Fatal("expected failure without change.json")
	}
	if _, err := os.Stat(filepath.Join(out, validation.VerdictFile)); err == nil {
		t.Fatal("a verdict was written despite the failure")
	}
}
