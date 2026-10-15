// Package scheduler executes a pipeline by running its steps through a
// runner.Runner, respecting needs and a parallelism limit.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/runner"
)

// Status is the outcome of one step.
type Status string

// Step outcomes.
const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	TimedOut  Status = "timed_out"
	Errored   Status = "errored"
	Skipped   Status = "skipped"
	Cancelled Status = "cancelled"

	inProgress Status = "running" // internal only, never in a Result
)

// Options configures one execution.
type Options struct {
	MaxParallel    int
	DefaultTimeout time.Duration
	Workspace      string
	CPUs           float64
	MemoryMB       int
	Secrets        map[string]string           // secret values by name
	Log            func(step string) io.Writer // nil discards logs
}

// StepResult is one step's outcome.
type StepResult struct {
	Name     string
	Status   Status
	ExitCode int
	Duration time.Duration
	Err      error // set for errored steps
}

// Result lists every step's outcome in file order.
type Result struct {
	Steps []StepResult
}

// Succeeded reports whether every step succeeded.
func (r Result) Succeeded() bool {
	for _, s := range r.Steps {
		if s.Status != Succeeded {
			return false
		}
	}
	return true
}

type finished struct {
	index  int
	result StepResult
}

// Execute runs p to completion or cancellation. It never returns an error:
// every problem is recorded as a step outcome.
func Execute(ctx context.Context, p *pipeline.Pipeline, r runner.Runner, o Options) Result {
	steps := p.Steps
	results := make([]StepResult, len(steps))
	index := make(map[string]int, len(steps))
	for i, st := range steps {
		index[st.Name] = i
		results[i].Name = st.Name
	}
	done := make(chan finished)
	running := 0

	for {
		if ctx.Err() == nil {
			markSkipped(steps, results, index)
			for i, st := range steps {
				if running >= o.MaxParallel {
					break
				}
				if results[i].Status != "" || !needsSucceeded(st, results, index) {
					continue
				}
				job, err := buildJob(st, o)
				if err != nil {
					results[i].Status, results[i].Err = Errored, err
					continue
				}
				results[i].Status = inProgress
				running++
				go func(i int) { done <- finished{i, runStep(ctx, r, job)} }(i)
			}
		}
		if running == 0 {
			break
		}
		select {
		case f := <-done:
			results[f.index] = f.result
			running--
		case <-ctx.Done():
			f := <-done
			results[f.index] = f.result
			running--
		}
	}

	for i := range results {
		if results[i].Status == "" {
			if ctx.Err() != nil {
				results[i].Status = Cancelled
			} else {
				results[i].Status = Skipped
			}
		}
	}
	return Result{Steps: results}
}

// markSkipped marks pending steps whose needs can no longer succeed,
// repeating until nothing changes so skips cascade.
func markSkipped(steps []pipeline.Step, results []StepResult, index map[string]int) {
	for changed := true; changed; {
		changed = false
		for i, st := range steps {
			if results[i].Status != "" {
				continue
			}
			for _, n := range st.Needs {
				s := results[index[n]].Status
				if s != "" && s != inProgress && s != Succeeded {
					results[i].Status = Skipped
					changed = true
					break
				}
			}
		}
	}
}

func needsSucceeded(st pipeline.Step, results []StepResult, index map[string]int) bool {
	for _, n := range st.Needs {
		if results[index[n]].Status != Succeeded {
			return false
		}
	}
	return true
}

// buildJob turns a step into a runner job, resolving its secrets.
func buildJob(st pipeline.Step, o Options) (runner.Job, error) {
	env := maps.Clone(st.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, name := range st.Secrets {
		v, ok := o.Secrets[name]
		if !ok {
			return runner.Job{}, fmt.Errorf("secret %s is not set in the secrets file", name)
		}
		env[name] = v
	}
	timeout := st.Timeout
	if timeout == 0 {
		timeout = o.DefaultTimeout
	}
	var log io.Writer = io.Discard
	if o.Log != nil {
		if w := o.Log(st.Name); w != nil {
			log = w
		}
	}
	return runner.Job{
		Name:      st.Name,
		Image:     st.Image,
		Script:    st.Run,
		Env:       env,
		Workspace: o.Workspace,
		Timeout:   timeout,
		CPUs:      o.CPUs,
		MemoryMB:  o.MemoryMB,
		Log:       log,
	}, nil
}

func runStep(ctx context.Context, r runner.Runner, job runner.Job) StepResult {
	res, err := r.Run(ctx, job)
	sr := StepResult{Name: job.Name, ExitCode: res.ExitCode, Duration: res.Duration}
	switch {
	case err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled)):
		sr.Status = Cancelled
	case err != nil:
		sr.Status, sr.Err = Errored, err
	case res.TimedOut:
		sr.Status = TimedOut
	case res.ExitCode == 0:
		sr.Status = Succeeded
	default:
		sr.Status = Failed
	}
	return sr
}
