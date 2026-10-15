//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

const testImage = "plumbix-reference-validator:test"

// buildImage builds the validator image from the repository root.
func buildImage(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "build", "-q", "-f", "validators/reference/Dockerfile", "-t", testImage, ".")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
}

// TestImageRunsAsValidator runs the real image the way a pipeline will:
// inputs and workspace read-only, output writable.
func TestImageRunsAsValidator(t *testing.T) {
	buildImage(t)
	in, out, ws := filepath.Join(t.TempDir(), "in"), t.TempDir(), t.TempDir()
	for _, dir := range []string{out, ws} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("Uploads retry three times.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crit := []change.Criterion{
		{ID: "AC-1", Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}},
		{ID: "AC-2", Text: "docs mention retries", Check: &change.Check{Command: "grep -q retry README.md"}},
		{ID: "AC-3", Text: "workspace stays untouched", Check: &change.Check{Command: "touch should-fail"}},
		{ID: "AC-4", Text: "pleasant to use"},
	}
	c := change.Change{ID: "c_aaaaaaaaaaaa", Title: "t", Intent: "i", SourceBranch: "f", TargetBranch: "main", AcceptanceCriteria: crit}
	if err := validation.PrepareInputs(in, validation.NewInput(c, "b", "h", []string{"upload_test.go"}, 1, nil), []validation.StepEvidence{{Step: "test", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}

	d, err := runner.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var log bytes.Buffer
	res, err := d.Run(context.Background(), runner.Job{
		Name: "reference-validator-test", Image: testImage, Workspace: ws, WorkspaceReadOnly: true,
		Mounts: []runner.Mount{
			{Source: in, Target: validation.InPath, ReadOnly: true},
			{Source: out, Target: validation.OutPath},
		},
		Timeout: 2 * time.Minute, CPUs: 1, MemoryMB: 128, Log: &log,
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("validator run: %+v, %v\n%s", res, err, log.String())
	}
	v, err := validation.ReadVerdict(filepath.Join(out, validation.VerdictFile), crit)
	if err != nil {
		t.Fatalf("Plumbix rejected the verdict: %v\nlog: %s", err, log.String())
	}
	got := map[string]validation.Status{}
	for _, cv := range v.Criteria {
		got[cv.ID] = cv.Status
	}
	want := map[string]validation.Status{"AC-1": validation.Met, "AC-2": validation.Met, "AC-3": validation.Unmet, "AC-4": validation.Unknown}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("%s = %s, want %s", id, got[id], st)
		}
	}
	if v.Outcome != validation.Fix || len(v.Risks) != 1 || v.Risks[0].Area != "tests" {
		t.Errorf("outcome %s, risks %+v", v.Outcome, v.Risks)
	}
}

// TestEnvFileNeverEntersBuildContext proves .dockerignore keeps secrets out.
func TestEnvFileNeverEntersBuildContext(t *testing.T) {
	data, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{".env", ".env.*", ".git", "bin"} {
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == pattern {
				found = true
			}
		}
		if !found {
			t.Errorf(".dockerignore is missing %q", pattern)
		}
	}
	buildImage(t)
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", testImage, "-c", "find / -name '.env*' -not -path '/proc/*' 2>/dev/null; true").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect image: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("image contains env files:\n%s", out)
	}
}
