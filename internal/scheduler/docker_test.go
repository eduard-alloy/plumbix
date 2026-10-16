//go:build integration

package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/runner"
)

// timingRunner records when each job ran on the wrapped runner.
type timingRunner struct {
	inner      runner.Runner
	mu         sync.Mutex
	start, end map[string]time.Time
}

func (r *timingRunner) Run(ctx context.Context, job runner.Job) (runner.Result, error) {
	r.mu.Lock()
	r.start[job.Name] = time.Now()
	r.mu.Unlock()
	res, err := r.inner.Run(ctx, job)
	r.mu.Lock()
	r.end[job.Name] = time.Now()
	r.mu.Unlock()
	return res, err
}

// TestRealContainersRunInParallel runs two independent 3-second steps and a
// dependent step on Docker. It checks that the independent steps overlapped
// in time, which holds however slowly a busy Docker engine starts them.
func TestRealContainersRunInParallel(t *testing.T) {
	d, err := runner.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tr := &timingRunner{inner: d, start: map[string]time.Time{}, end: map[string]time.Time{}}
	p := mustParse(t, `  - {name: a, image: "alpine:3.20", run: "sleep 3"}
  - {name: b, image: "alpine:3.20", run: "sleep 3"}
  - {name: c, image: "alpine:3.20", run: "true", needs: [a, b]}
`)
	o := Options{MaxParallel: 4, DefaultTimeout: time.Minute, Workspace: t.TempDir(), CPUs: 0.5, MemoryMB: 64}
	res := Execute(context.Background(), p, tr, o)
	if !res.Succeeded() {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	if !(tr.start["b"].Before(tr.end["a"]) && tr.start["a"].Before(tr.end["b"])) {
		t.Errorf("a and b did not overlap: a %s-%s, b %s-%s", tr.start["a"], tr.end["a"], tr.start["b"], tr.end["b"])
	}
	if tr.start["c"].Before(tr.end["a"]) || tr.start["c"].Before(tr.end["b"]) {
		t.Error("c started before its needs finished")
	}
}
