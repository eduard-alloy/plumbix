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
	// Setup, when set, runs just before a step starts. It sees the steps
	// finished so far and can add mounts, for example validator inputs.
	Setup func(step pipeline.Step, finished []StepResult) (StepSetup, error)
	// Finished, when set, is called exactly once per step with its index in
	// the pipeline and its final result, as soon as it is known: after the
	// step ran, when it was skipped, or when it could not be built.
	Finished func(index int, step pipeline.Step, result StepResult)
}

// StepSetup is extra configuration for one step.
type StepSetup struct {
	Mounts            []runner.Mount
	WorkspaceReadOnly bool
	Env               map[string]string // added to the step's env; values the step or its secrets set win
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
	// settle records a step's final result and tells the Finished hook.
	settle := func(i int, r StepResult) {
		results[i] = r
		if o.Finished != nil {
			o.Finished(i, steps[i], r)
		}
	}

	for {
		if ctx.Err() == nil {
			markSkipped(steps, results, index, settle)
			for i, st := range steps {
				if running >= o.MaxParallel {
					break
				}
				if results[i].Status != "" || !needsSatisfied(st, results, index) {
					continue
				}
				job, err := buildJob(st, o, finishedSoFar(results))
				if err != nil {
					settle(i, StepResult{Name: st.Name, Status: Errored, Err: err})
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
			settle(f.index, f.result)
			running--
		case <-ctx.Done():
			f := <-done
			settle(f.index, f.result)
			running--
		}
	}

	for i := range results {
		if results[i].Status == "" {
			if ctx.Err() != nil {
				settle(i, StepResult{Name: steps[i].Name, Status: Cancelled})
			} else {
				settle(i, StepResult{Name: steps[i].Name, Status: Skipped})
			}
		}
	}
	return Result{Steps: results}
}

// markSkipped marks pending steps whose needs can no longer succeed,
// repeating until nothing changes so skips cascade. Validators are never
// skipped: they judge the evidence, including failures.
func markSkipped(steps []pipeline.Step, results []StepResult, index map[string]int, settle func(int, StepResult)) {
	for changed := true; changed; {
		changed = false
		for i, st := range steps {
			if results[i].Status != "" || st.Validator {
				continue
			}
			for _, n := range st.Needs {
				s := results[index[n]].Status
				if s != "" && s != inProgress && s != Succeeded {
					settle(i, StepResult{Name: st.Name, Status: Skipped})
					changed = true
					break
				}
			}
		}
	}
}

// needsSatisfied reports whether a step may start: normal steps need every
// need to have succeeded, validators need every need to have finished.
func needsSatisfied(st pipeline.Step, results []StepResult, index map[string]int) bool {
	for _, n := range st.Needs {
		s := results[index[n]].Status
		if st.Validator {
			if s == "" || s == inProgress {
				return false
			}
		} else if s != Succeeded {
			return false
		}
	}
	return true
}

// finishedSoFar returns the results of steps that have ended.
func finishedSoFar(results []StepResult) []StepResult {
	var out []StepResult
	for _, r := range results {
		if r.Status != "" && r.Status != inProgress {
			out = append(out, r)
		}
	}
	return out
}

// buildJob turns a step into a runner job, resolving its secrets.
func buildJob(st pipeline.Step, o Options, finished []StepResult) (runner.Job, error) {
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
	var setup StepSetup
	if o.Setup != nil {
		var err error
		if setup, err = o.Setup(st, finished); err != nil {
			return runner.Job{}, fmt.Errorf("prepare step %s: %w", st.Name, err)
		}
	}
	for k, v := range setup.Env {
		if _, present := env[k]; !present {
			env[k] = v
		}
	}
	return runner.Job{
		Mounts:            setup.Mounts,
		WorkspaceReadOnly: setup.WorkspaceReadOnly,
		Name:              st.Name,
		Image:             st.Image,
		Script:            st.Run,
		Env:               env,
		Workspace:         o.Workspace,
		Timeout:           timeout,
		CPUs:              o.CPUs,
		MemoryMB:          o.MemoryMB,
		Log:               log,
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
