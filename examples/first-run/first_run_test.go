//go:build integration

// Package firstrun holds the first-run example: a sample repository, a
// pipeline, a criteria set, a change, and a script that takes a machine
// with Docker from nothing to an approved verdict and a merge. This test
// runs the script the way a new user would, against a binary built from
// this tree, so the example cannot rot. Run with make integration.
package firstrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// binary builds plumbix from this tree into a temporary directory, once
// Docker is known to be reachable; without Docker the test skips.
func binary(t *testing.T) (bin, dir string) {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("no Docker client: %v", err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Skipf("Docker is not reachable: %v", err)
	}
	tmp := t.TempDir()
	bin = filepath.Join(tmp, "plumbix")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/plumbix")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin, filepath.Join(tmp, "first-run")
}

// run executes the script with the given arguments and extra environment
// and returns its output; it fails the test on a non-zero exit.
func run(t *testing.T, bin, dir, port string, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "./first-run.sh", args...)
	cmd.Env = append(append(os.Environ(),
		"PLUMBIX_BIN="+bin,
		"PLUMBIX_EXAMPLE_DIR="+dir,
		"PLUMBIX_EXAMPLE_PORT="+port,
	), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("script output:\n%s", out)
	if err != nil {
		t.Fatalf("first-run.sh: %v", err)
	}
	return string(out)
}

func expect(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
}

func TestFirstRunScriptReachesAMerge(t *testing.T) {
	bin, dir := binary(t)
	out := run(t, bin, dir, "6412", nil, "--stop")
	expect(t, out, "verdict: approved", "state: merged", "first run complete")
}

// The default run leaves its server up for the person to look at; running
// the script again must stop that server first rather than delete its
// data under it and then talk to it.
func TestFirstRunCanRunAgainOverItsOwnServer(t *testing.T) {
	bin, dir := binary(t)
	first := run(t, bin, dir, "6413", nil)
	expect(t, first, "state: merged", "The server is still running")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(filepath.Join(dir, "serve.pid")); err == nil {
			_ = exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
	})
	second := run(t, bin, dir, "6413", nil, "--stop")
	expect(t, second, "stopping the server a previous run left", "verdict: approved", "state: merged")
}

// The branch that builds the validator image through the server, which a
// machine that already has the image never takes on its own.
func TestFirstRunBuildsTheValidatorImage(t *testing.T) {
	bin, dir := binary(t)
	out := run(t, bin, dir, "6414", []string{"PLUMBIX_EXAMPLE_BUILD_IMAGE=yes"}, "--stop")
	expect(t, out, "building plumbix-reference-validator:dev", "built", "verdict: approved", "state: merged")
}
