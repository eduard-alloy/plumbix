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
	// ChangedFiles are the paths a change touched, used for when.paths.
	// ChangesKnown says the list is meaningful; a plain branch run has
	// nothing to compare, so conditional steps run.
	ChangedFiles []string
	ChangesKnown bool
	// Setup, when set, runs just before a step starts. It sees the steps
	// finished so far and can add mounts, for example validator inputs.
	Setup func(step pipeline.Step, finished []StepResult) (StepSetup, error)
	// External runs an external validator step in place of the runner
	// (WU-023): it publishes the review and waits for the verdict. ctx ends
	// at the step's timeout; the hook returns the step's result and may
	// write to log. An external step with no hook errors.
	External func(ctx context.Context, step pipeline.Step, setup StepSetup, log io.Writer) StepResult
	// ExternalTimeout bounds an external step that sets no timeout; zero
	// means DefaultExternalTimeout.
	ExternalTimeout time.Duration
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

// DefaultExternalTimeout is how long an external step waits for a verdict
// when neither the step nor the options say.
const DefaultExternalTimeout = time.Hour

// Reasons a step was skipped.
const (
	ReasonNeeds = "needs" // a step it needs did not succeed
	ReasonPaths = "paths" // no changed file matched its when.paths
)

// StepResult is one step's outcome.
type StepResult struct {
	Name     string
	Status   Status
	ExitCode int
	Duration time.Duration
	Err      error // set for errored steps
	// Attempts is how many times the step ran; above one after retries.
	Attempts int
	// Allowed marks a failure the step's allow_failure tolerates.
	Allowed bool
	// Reason says why a step was skipped: ReasonNeeds or ReasonPaths.
	Reason string
}

// satisfied reports whether a step that needs this one may start: it
// succeeded, its failure was allowed, or its paths did not change.
func (s StepResult) satisfied() bool {
	return s.Status == Succeeded || s.Allowed || (s.Status == Skipped && s.Reason == ReasonPaths)
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

// Acceptable reports whether every step succeeded, failed with
// allow_failure, or was skipped because its paths did not change.
func (r Result) Acceptable() bool {
	for _, s := range r.Steps {
		if !s.satisfied() {
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
	running := 0    // steps in flight, external ones included
	containers := 0 // steps holding a container slot; a waiting external step holds none
	// settle records a step's final result and tells the Finished hook.
	settle := func(i int, r StepResult) {
		results[i] = r
		if o.Finished != nil {
			o.Finished(i, steps[i], r)
		}
	}

	for i, st := range steps {
		if !st.Validator && len(st.OnlyPaths) > 0 && o.ChangesKnown && !anyMatch(st.OnlyPaths, o.ChangedFiles) {
			settle(i, StepResult{Name: st.Name, Status: Skipped, Reason: ReasonPaths})
		}
	}

	for {
		if ctx.Err() == nil {
			markSkipped(steps, results, index, settle)
			for i, st := range steps {
				if results[i].Status != "" || !needsSatisfied(st, results, index) {
					continue
				}
				if st.External {
					setup, log, err := prepareExternal(st, o, finishedSoFar(results))
					if err != nil {
						settle(i, StepResult{Name: st.Name, Status: Errored, Err: err})
						continue
					}
					results[i].Status = inProgress
					running++
					go func(i int, st pipeline.Step) { done <- finished{i, runExternal(ctx, o, st, setup, log)} }(i, st)
					continue
				}
				if containers >= o.MaxParallel {
					continue
				}
				job, err := buildJob(st, o, finishedSoFar(results))
				if err != nil {
					settle(i, StepResult{Name: st.Name, Status: Errored, Err: err})
					continue
				}
				results[i].Status = inProgress
				running++
				containers++
				go func(i int, st pipeline.Step) { done <- finished{i, runStep(ctx, r, job, st)} }(i, st)
			}
		}
		if running == 0 {
			break
		}
		var f finished
		select {
		case f = <-done:
		case <-ctx.Done():
			f = <-done
		}
		settle(f.index, f.result)
		running--
		if !steps[f.index].External {
			containers--
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
				need := results[index[n]]
				if need.Status != "" && need.Status != inProgress && !need.satisfied() {
					settle(i, StepResult{Name: st.Name, Status: Skipped, Reason: ReasonNeeds})
					changed = true
					break
				}
			}
		}
	}
}

// needsSatisfied reports whether a step may start: normal steps need every
// need satisfied (succeeded, failure allowed, or skipped by its paths),
// validators need every need to have finished.
func needsSatisfied(st pipeline.Step, results []StepResult, index map[string]int) bool {
	for _, n := range st.Needs {
		need := results[index[n]]
		if st.Validator {
			if need.Status == "" || need.Status == inProgress {
				return false
			}
		} else if !need.satisfied() {
			return false
		}
	}
	return true
}

// anyMatch reports whether any changed file matches one of the globs.
func anyMatch(globs, files []string) bool {
	for _, f := range files {
		for _, g := range globs {
			if pipeline.MatchPath(g, f) {
				return true
			}
		}
	}
	return false
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

// prepareExternal runs the Setup hook for an external step and opens its log.
func prepareExternal(st pipeline.Step, o Options, finished []StepResult) (StepSetup, io.Writer, error) {
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
			return StepSetup{}, nil, fmt.Errorf("prepare step %s: %w", st.Name, err)
		}
	}
	return setup, log, nil
}

// runExternal hands an external step to the External hook under the step's
// timeout. A missing hook, or a hook that returns no status, is an error:
// an external step never passes by default.
func runExternal(ctx context.Context, o Options, st pipeline.Step, setup StepSetup, log io.Writer) StepResult {
	if o.External == nil {
		return StepResult{Name: st.Name, Status: Errored, Err: fmt.Errorf("external step %s: this server has no external reviews", st.Name)}
	}
	timeout := st.Timeout
	if timeout == 0 {
		timeout = o.ExternalTimeout
	}
	if timeout == 0 {
		timeout = DefaultExternalTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	sr := o.External(stepCtx, st, setup, log)
	sr.Name = st.Name
	if sr.Duration == 0 {
		sr.Duration = time.Since(start)
	}
	if sr.Attempts == 0 {
		sr.Attempts = 1
	}
	switch {
	case sr.Status != "":
	case ctx.Err() != nil:
		sr.Status = Cancelled
	case stepCtx.Err() != nil:
		sr.Status = TimedOut
	default:
		sr.Status, sr.Err = Errored, fmt.Errorf("external step %s returned no status", st.Name)
	}
	return sr
}

// runStep runs a job, again after a failure or timeout while the step has
// retries left. Only the last attempt's outcome is reported; the log gets
// a separator before each later attempt.
func runStep(ctx context.Context, r runner.Runner, job runner.Job, st pipeline.Step) StepResult {
	total := st.Retries + 1
	var sr StepResult
	for attempt := 1; attempt <= total; attempt++ {
		if attempt > 1 {
			fmt.Fprintf(job.Log, "\n--- plumbix: attempt %d of %d ---\n", attempt, total)
		}
		sr = runOnce(ctx, r, job)
		sr.Attempts = attempt
		if (sr.Status != Failed && sr.Status != TimedOut) || ctx.Err() != nil {
			break
		}
	}
	if st.AllowFailure && (sr.Status == Failed || sr.Status == TimedOut) {
		sr.Allowed = true
	}
	return sr
}

func runOnce(ctx context.Context, r runner.Runner, job runner.Job) StepResult {
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
