package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/runner"
)

// fakeRunner runs jobs through a per-step behaviour function and records
// concurrency and timing.
type fakeRunner struct {
	mu      sync.Mutex
	behave  func(ctx context.Context, job runner.Job) (runner.Result, error)
	jobs    map[string]runner.Job
	start   map[string]time.Time
	end     map[string]time.Time
	running int
	maxSeen int
}

func newFake(behave func(ctx context.Context, job runner.Job) (runner.Result, error)) *fakeRunner {
	return &fakeRunner{behave: behave, jobs: map[string]runner.Job{}, start: map[string]time.Time{}, end: map[string]time.Time{}}
}

func (f *fakeRunner) Run(ctx context.Context, job runner.Job) (runner.Result, error) {
	f.mu.Lock()
	f.jobs[job.Name] = job
	f.start[job.Name] = time.Now()
	f.running++
	if f.running > f.maxSeen {
		f.maxSeen = f.running
	}
	f.mu.Unlock()
	res, err := f.behave(ctx, job)
	f.mu.Lock()
	f.running--
	f.end[job.Name] = time.Now()
	f.mu.Unlock()
	return res, err
}

func ok(context.Context, runner.Job) (runner.Result, error) { return runner.Result{}, nil }

func mustParse(t *testing.T, src string) *pipeline.Pipeline {
	t.Helper()
	p, err := pipeline.Parse([]byte("version: 1\nsteps:\n" + src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(maxParallel int) Options {
	return Options{MaxParallel: maxParallel, DefaultTimeout: time.Minute, Workspace: "/ws", CPUs: 1, MemoryMB: 256}
}

func statusOf(t *testing.T, res Result) map[string]Status {
	t.Helper()
	m := map[string]Status{}
	for _, s := range res.Steps {
		m[s.Name] = s.Status
	}
	return m
}

func TestIndependentStepsRunConcurrently(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x}\n")
	var started sync.WaitGroup
	started.Add(2)
	fake := newFake(func(ctx context.Context, job runner.Job) (runner.Result, error) {
		started.Done()
		done := make(chan struct{})
		go func() { started.Wait(); close(done) }()
		select {
		case <-done:
			return runner.Result{}, nil
		case <-time.After(3 * time.Second):
			return runner.Result{ExitCode: 99}, nil
		}
	})
	res := Execute(context.Background(), p, fake, opts(2))
	if got := statusOf(t, res); got["a"] != Succeeded || got["b"] != Succeeded {
		t.Fatalf("statuses = %v, want both succeeded (steps did not overlap)", got)
	}
}

func TestMaxParallelOneSerializes(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x}\n  - {name: c, image: x, run: x}\n")
	fake := newFake(func(context.Context, runner.Job) (runner.Result, error) {
		time.Sleep(20 * time.Millisecond)
		return runner.Result{}, nil
	})
	Execute(context.Background(), p, fake, opts(1))
	if fake.maxSeen != 1 {
		t.Fatalf("max concurrent = %d, want 1", fake.maxSeen)
	}
}

func TestNeedsAreRespected(t *testing.T) {
	p := mustParse(t, "  - {name: c, image: x, run: x, needs: [a, b]}\n  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x}\n")
	fake := newFake(func(context.Context, runner.Job) (runner.Result, error) {
		time.Sleep(20 * time.Millisecond)
		return runner.Result{}, nil
	})
	res := Execute(context.Background(), p, fake, opts(4))
	if got := statusOf(t, res); got["c"] != Succeeded {
		t.Fatalf("statuses = %v", got)
	}
	for _, need := range []string{"a", "b"} {
		if fake.start["c"].Before(fake.end[need]) {
			t.Errorf("c started before %s finished", need)
		}
	}
	if names := []string{res.Steps[0].Name, res.Steps[1].Name, res.Steps[2].Name}; names[0] != "c" || names[1] != "a" || names[2] != "b" {
		t.Errorf("results not in file order: %v", names)
	}
}

func TestFailureSkipsDependents(t *testing.T) {
	p := mustParse(t, `  - {name: a, image: x, run: x}
  - {name: b, image: x, run: x, needs: [a]}
  - {name: c, image: x, run: x}
  - {name: d, image: x, run: x, needs: [b]}
`)
	fake := newFake(func(_ context.Context, job runner.Job) (runner.Result, error) {
		if job.Name == "a" {
			return runner.Result{ExitCode: 1}, nil
		}
		return runner.Result{}, nil
	})
	got := statusOf(t, Execute(context.Background(), p, fake, opts(4)))
	want := map[string]Status{"a": Failed, "b": Skipped, "c": Succeeded, "d": Skipped}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if _, ran := fake.jobs["b"]; ran {
		t.Error("skipped step b was run")
	}
}

func TestStatusMapping(t *testing.T) {
	p := mustParse(t, "  - {name: slow, image: x, run: x}\n  - {name: broken, image: x, run: x}\n  - {name: fine, image: x, run: x}\n")
	fake := newFake(func(_ context.Context, job runner.Job) (runner.Result, error) {
		switch job.Name {
		case "slow":
			return runner.Result{TimedOut: true, ExitCode: -1}, nil
		case "broken":
			return runner.Result{}, errors.New("engine unavailable")
		}
		return runner.Result{ExitCode: 0}, nil
	})
	res := Execute(context.Background(), p, fake, opts(4))
	got := statusOf(t, res)
	if got["slow"] != TimedOut || got["broken"] != Errored || got["fine"] != Succeeded {
		t.Fatalf("statuses = %v", got)
	}
	for _, s := range res.Steps {
		if s.Name == "broken" && (s.Err == nil || !strings.Contains(s.Err.Error(), "engine unavailable")) {
			t.Errorf("broken step error = %v", s.Err)
		}
	}
	if res.Succeeded() {
		t.Error("Succeeded() = true with failed steps")
	}
}

func TestCancelStopsRun(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x, needs: [a]}\n")
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFake(func(ctx context.Context, _ runner.Job) (runner.Result, error) {
		<-ctx.Done()
		return runner.Result{}, ctx.Err()
	})
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	done := make(chan Result, 1)
	go func() { done <- Execute(ctx, p, fake, opts(4)) }()
	select {
	case res := <-done:
		got := statusOf(t, res)
		if got["a"] != Cancelled || got["b"] != Cancelled {
			t.Fatalf("statuses = %v, want both cancelled", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after cancel")
	}
}

func TestJobFields(t *testing.T) {
	p := mustParse(t, `  - name: a
    image: golang:1.27
    run: go test ./...
    env: {MODE: ci}
    secrets: [TOKEN]
  - {name: b, image: x, run: x, timeout: 5s}
  - {name: v, image: val, validator: true}
`)
	fake := newFake(ok)
	o := opts(4)
	o.Secrets = map[string]string{"TOKEN": "s3cret"}
	res := Execute(context.Background(), p, fake, o)
	if !res.Succeeded() {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	a := fake.jobs["a"]
	if a.Image != "golang:1.27" || a.Script != "go test ./..." || a.Workspace != "/ws" || a.CPUs != 1 || a.MemoryMB != 256 {
		t.Errorf("job a = %+v", a)
	}
	if a.Env["MODE"] != "ci" || a.Env["TOKEN"] != "s3cret" || len(a.Env) != 2 {
		t.Errorf("job a env = %v", a.Env)
	}
	if a.Timeout != time.Minute || fake.jobs["b"].Timeout != 5*time.Second {
		t.Errorf("timeouts a=%s b=%s", a.Timeout, fake.jobs["b"].Timeout)
	}
	if fake.jobs["v"].Script != "" {
		t.Errorf("validator script = %q, want empty (entrypoint)", fake.jobs["v"].Script)
	}
	if _, leaked := fake.jobs["b"].Env["TOKEN"]; leaked {
		t.Error("secret leaked to a step that did not ask for it")
	}
}

func TestMissingSecretErrorsStep(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x, secrets: [TOKEN]}\n")
	fake := newFake(ok)
	res := Execute(context.Background(), p, fake, opts(1))
	s := res.Steps[0]
	if s.Status != Errored || s.Err == nil || !strings.Contains(s.Err.Error(), "TOKEN") {
		t.Fatalf("step = %+v, want errored naming the missing secret", s)
	}
	if strings.Contains(s.Err.Error(), "s3cret") {
		t.Fatal("error leaks a secret value")
	}
	if _, ran := fake.jobs["a"]; ran {
		t.Error("step ran without its secret")
	}
}

func TestSetupHook(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x}\n  - {name: v, image: val, validator: true, needs: [a, b]}\n")
	fake := newFake(ok)
	o := opts(1)
	var seen []string
	o.Setup = func(st pipeline.Step, finished []StepResult) (StepSetup, error) {
		if st.Name != "v" {
			return StepSetup{}, nil
		}
		for _, f := range finished {
			seen = append(seen, f.Name+"="+string(f.Status))
		}
		return StepSetup{Mounts: []runner.Mount{{Source: "/in", Target: "/plumbix/in", ReadOnly: true}}, WorkspaceReadOnly: true}, nil
	}
	res := Execute(context.Background(), p, fake, o)
	if !res.Succeeded() {
		t.Fatalf("run failed: %+v", res.Steps)
	}
	if strings.Join(seen, ",") != "a=succeeded,b=succeeded" {
		t.Errorf("setup saw %v, want both needs finished", seen)
	}
	v := fake.jobs["v"]
	if !v.WorkspaceReadOnly || len(v.Mounts) != 1 || v.Mounts[0].Target != "/plumbix/in" {
		t.Errorf("validator job = %+v", v)
	}
	if fake.jobs["a"].WorkspaceReadOnly {
		t.Error("normal step got a read-only workspace")
	}
}

func TestSetupErrorErrorsStep(t *testing.T) {
	p := mustParse(t, "  - {name: a, image: x, run: x}\n")
	o := opts(1)
	o.Setup = func(pipeline.Step, []StepResult) (StepSetup, error) {
		return StepSetup{}, errors.New("cannot prepare inputs")
	}
	fake := newFake(ok)
	res := Execute(context.Background(), p, fake, o)
	if s := res.Steps[0]; s.Status != Errored || !strings.Contains(s.Err.Error(), "cannot prepare") {
		t.Fatalf("step = %+v", s)
	}
	if _, ran := fake.jobs["a"]; ran {
		t.Error("step ran after its setup failed")
	}
}

func TestValidatorRunsAfterFailedNeed(t *testing.T) {
	p := mustParse(t, `  - {name: test, image: x, run: x}
  - {name: build, image: x, run: x, needs: [test]}
  - {name: v, image: val, validator: true, needs: [test, build]}
`)
	fake := newFake(func(_ context.Context, job runner.Job) (runner.Result, error) {
		if job.Name == "test" {
			return runner.Result{ExitCode: 1}, nil
		}
		return runner.Result{}, nil
	})
	got := statusOf(t, Execute(context.Background(), p, fake, opts(2)))
	if got["test"] != Failed || got["build"] != Skipped || got["v"] != Succeeded {
		t.Fatalf("statuses = %v; validators must run once their needs have finished, whatever the outcome", got)
	}
	if fake.start["v"].Before(fake.end["test"]) {
		t.Error("validator started before its need finished")
	}
}
