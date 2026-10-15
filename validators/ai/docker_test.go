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

	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// TestImageFailsCleanlyWithoutKey builds the image and runs it with no API
// key: it must exit non-zero, explain why, and write no verdict. No API cost.
func TestImageFailsCleanlyWithoutKey(t *testing.T) {
	const image = "plumbix-ai-validator:test"
	build := exec.Command("docker", "build", "-q", "-f", "validators/ai/Dockerfile", "-t", image, ".")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	in, out, ws := setup(t)
	for _, dir := range []string{out, ws} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	d, err := runner.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var log bytes.Buffer
	res, err := d.Run(context.Background(), runner.Job{
		Name: "ai-validator-test", Image: image, Workspace: ws, WorkspaceReadOnly: true,
		Mounts:  []runner.Mount{{Source: in, Target: validation.InPath, ReadOnly: true}, {Source: out, Target: validation.OutPath}},
		Timeout: time.Minute, CPUs: 1, MemoryMB: 128, Log: &log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || !strings.Contains(log.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("exit %d, log %q; want a failure naming the missing key", res.ExitCode, log.String())
	}
	if _, err := os.Stat(filepath.Join(out, validation.VerdictFile)); err == nil {
		t.Fatal("verdict written without a key")
	}
}
