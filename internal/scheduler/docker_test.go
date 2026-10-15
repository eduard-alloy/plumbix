//go:build integration

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/runner"
)

// TestRealContainersRunInParallel runs two independent 3-second steps and a
// dependent step on Docker, and checks the independent ones overlapped.
func TestRealContainersRunInParallel(t *testing.T) {
	d, err := runner.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p := mustParse(t, `  - {name: a, image: "alpine:3.20", run: "sleep 3"}
  - {name: b, image: "alpine:3.20", run: "sleep 3"}
  - {name: c, image: "alpine:3.20", run: "true", needs: [a, b]}
`)
	o := Options{MaxParallel: 4, DefaultTimeout: time.Minute, Workspace: t.TempDir(), CPUs: 0.5, MemoryMB: 64}
	start := time.Now()
	res := Execute(context.Background(), p, d, o)
	elapsed := time.Since(start)
	if !res.Succeeded() {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	if elapsed > 5500*time.Millisecond {
		t.Errorf("took %s; two 3s steps in parallel plus a short step should take well under 6s", elapsed)
	}
}
