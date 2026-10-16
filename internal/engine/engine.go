// Package engine runs the validation loop: for a change it fetches the
// code, runs the pipeline, gives validators their inputs, combines their
// verdicts and moves the change to its outcome state.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/scheduler"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// PipelineFile is the pipeline's path inside a repository.
const PipelineFile = ".plumbix/pipeline.yaml"

// Folders inside the data directory.
const (
	mirrorsDir  = "mirrors"
	workDir     = "work"
	evidenceDir = "evidence"
)

// cleanupTimeout bounds database writes made after a run was cancelled.
const cleanupTimeout = 30 * time.Second

var (
	errSuperseded = errors.New("superseded by a newer run")
	errShutdown   = errors.New("interrupted because the Plumbix server stopped")
	runIDRe       = regexp.MustCompile(`^run_[a-z2-7]{12}$`)
)

// Store is what the engine persists. Events for changes and runs are the
// store's own; the engine appends only step.finished, which no other write
// records.
type Store interface {
	change.Store
	run.Store
	events.Appender
}

// Config configures runs.
type Config struct {
	DataDir        string
	MaxParallel    int
	DefaultTimeout time.Duration
	CPUs           float64
	MemoryMB       int
	MaxFixRounds   int
	Secrets        map[string]string
	Hosts          host.Registry // credentials per git host; nil fetches anonymously
}

// Engine runs validations in the background.
type Engine struct {
	store  Store
	runner runner.Runner
	cfg    Config
	log    *slog.Logger
	now    func() time.Time

	base       context.Context
	stopAll    context.CancelCauseFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	active     map[string]activeRun // by change ID, or branchKey for plain runs
	merging    map[string]bool      // changes with a merge in progress
	beforePush func()               // test hook run between building and pushing a merge
	mirrorLock sync.Map             // repo ID -> *sync.Mutex
	askpass    string
	askOnce    sync.Once
	askErr     error
}

type activeRun struct {
	runID  string
	cancel context.CancelCauseFunc
}

// New returns an engine. Call Shutdown to stop it.
func New(store Store, r runner.Runner, cfg Config, log *slog.Logger) *Engine {
	base, stop := context.WithCancelCause(context.Background())
	return &Engine{store: store, runner: r, cfg: cfg, log: log, now: time.Now, base: base, stopAll: stop, active: map[string]activeRun{}}
}

// Validate starts a new iteration for a change and returns its run. The run
// continues in the background. A run still going for the change is cancelled.
func (e *Engine) Validate(ctx context.Context, changeID string) (run.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return run.Run{}, fmt.Errorf("%w: change %s", change.ErrNotFound, changeID)
		}
		return run.Run{}, err
	}
	if c.State.Final() {
		return run.Run{}, fmt.Errorf("%w: change %s is %s", change.ErrInvalidState, changeID, c.State)
	}
	if e.merging[changeID] {
		return run.Run{}, fmt.Errorf("%w: change %s is being merged", change.ErrInvalidState, changeID)
	}
	if prev, ok := e.active[changeID]; ok {
		prev.cancel(errSuperseded)
	}
	last, err := e.store.LastIteration(ctx, changeID)
	if err != nil {
		return run.Run{}, err
	}
	r := run.Run{ID: change.NewID("run_"), RepoID: c.RepoID, ChangeID: c.ID, Branch: c.SourceBranch, Iteration: last + 1, Status: run.Running, StartedAt: e.now().UTC(), Steps: []run.Step{}}
	if err := e.store.CreateRun(ctx, r); err != nil {
		return run.Run{}, err
	}
	if c.State != change.Running {
		if err := e.store.SetChangeState(ctx, c.ID, c.State, change.Running, e.now().UTC()); err != nil {
			return run.Run{}, fmt.Errorf("mark change running: %w", err)
		}
	}
	e.launch(changeID, r, func(ctx context.Context) ([]run.Step, *validation.Result, error) {
		return e.runPipeline(ctx, r, c)
	})
	return r, nil
}

// RunBranch starts a plain run of a branch's pipeline without its validators,
// for pushes that belong to no change. A plain run still going for the same
// branch is cancelled.
func (e *Engine) RunBranch(ctx context.Context, repoID, branch string) (run.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return run.Run{}, fmt.Errorf("%w: repo %s", change.ErrNotFound, repoID)
		}
		return run.Run{}, err
	}
	key := branchKey(repoID, branch)
	if prev, ok := e.active[key]; ok {
		prev.cancel(errSuperseded)
	}
	r := run.Run{ID: change.NewID("run_"), RepoID: repoID, Branch: branch, Status: run.Running, StartedAt: e.now().UTC(), Steps: []run.Step{}}
	if err := e.store.CreateRun(ctx, r); err != nil {
		return run.Run{}, err
	}
	e.launch(key, r, func(ctx context.Context) ([]run.Step, *validation.Result, error) {
		return e.runBranch(ctx, r, repo)
	})
	return r, nil
}

// ListHeads asks a repo's remote for its branch heads, with the host's
// credentials.
func (e *Engine) ListHeads(ctx context.Context, repo change.Repo) (map[string]string, error) {
	m, err := e.mirror(repo)
	if err != nil {
		return nil, err
	}
	return m.ListHeads(ctx)
}

// Commits fetches the repo and lists the first-parent commits from..to,
// newest first, or only to when from is empty.
func (e *Engine) Commits(ctx context.Context, repo change.Repo, from, to string) ([]gitops.Commit, error) {
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return nil, err
	}
	return m.Commits(ctx, from, to)
}

func branchKey(repoID, branch string) string { return "branch:" + repoID + ":" + branch }

// launch runs work in the background under key. Callers hold e.mu.
func (e *Engine) launch(key string, r run.Run, work func(context.Context) ([]run.Step, *validation.Result, error)) {
	runCtx, cancel := context.WithCancelCause(e.base)
	e.active[key] = activeRun{runID: r.ID, cancel: cancel}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel(nil)
		steps, result, err := work(runCtx)
		e.end(runCtx, r, steps, result, err)
		e.mu.Lock()
		if a, ok := e.active[key]; ok && a.runID == r.ID {
			delete(e.active, key)
		}
		e.mu.Unlock()
	}()
}

// mirror returns the repo's mirror, authenticated when the host has a token.
func (e *Engine) mirror(repo change.Repo) (*gitops.Mirror, error) {
	m := gitops.Open(filepath.Join(e.cfg.DataDir, mirrorsDir, repo.ID+".git"), repo.URL)
	if e.cfg.Hosts == nil {
		return m, nil
	}
	h, err := e.cfg.Hosts.For(repo)
	if err != nil {
		return nil, err
	}
	auth, ok := h.Auth(repo)
	if !ok {
		return m, nil
	}
	e.askOnce.Do(func() { e.askpass, e.askErr = gitops.EnsureAskpass(e.cfg.DataDir) })
	if e.askErr != nil {
		return nil, e.askErr
	}
	return m.WithAuth(auth, e.askpass), nil
}

// fetch updates the repo's mirror. Fetches of one repo never overlap.
func (e *Engine) fetch(ctx context.Context, repo change.Repo) (*gitops.Mirror, error) {
	m, err := e.mirror(repo)
	if err != nil {
		return nil, err
	}
	lock := e.repoLock(repo.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := m.Fetch(ctx); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", repo.URL, err)
	}
	return m, nil
}

// runBranch runs a branch's pipeline without validators.
func (e *Engine) runBranch(ctx context.Context, r run.Run, repo change.Repo) ([]run.Step, *validation.Result, error) {
	work := filepath.Join(e.cfg.DataDir, workDir, r.ID)
	defer os.RemoveAll(work)
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	head, err := m.Head(ctx, r.Branch)
	if err != nil {
		return nil, nil, err
	}
	if err := e.store.SetRunCommits(ctx, r.ID, head, ""); err != nil {
		return nil, nil, err
	}
	p, err := e.readPipeline(ctx, m, head, r.Branch)
	if err != nil {
		return nil, nil, err
	}
	workspace := filepath.Join(work, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, nil, err
	}
	remove, err := m.Worktree(ctx, head, workspace)
	if err != nil {
		return nil, nil, err
	}
	defer remove()
	plain := p.WithoutValidators()
	logs := map[string]*os.File{}
	defer closeLogs(logs)
	res := scheduler.Execute(ctx, plain, e.runner, e.schedulerOptions(r, workspace, logs))
	return toSteps(plain, res), nil, nil
}

// readPipeline reads and parses the pipeline file at a commit.
func (e *Engine) readPipeline(ctx context.Context, m *gitops.Mirror, commit, branch string) (*pipeline.Pipeline, error) {
	src, ok, err := m.Show(ctx, commit, PipelineFile)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no %s at %s on %s", PipelineFile, short(commit), branch)
	}
	p, err := pipeline.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %w", PipelineFile, short(commit), err)
	}
	return p, nil
}

// schedulerOptions returns the options shared by every run.
func (e *Engine) schedulerOptions(r run.Run, workspace string, logs map[string]*os.File) scheduler.Options {
	return scheduler.Options{
		MaxParallel: e.cfg.MaxParallel, DefaultTimeout: e.cfg.DefaultTimeout,
		Workspace: workspace, CPUs: e.cfg.CPUs, MemoryMB: e.cfg.MemoryMB, Secrets: e.cfg.Secrets,
		Log: func(step string) io.Writer {
			f, err := e.openLog(r.ID, step)
			if err != nil {
				e.log.Error("open step log", "run", r.ID, "step", step, "err", err)
				return nil
			}
			logs[step] = f
			return f
		},
		Finished: func(st pipeline.Step, res scheduler.StepResult) { e.recordStep(r, st, res) },
	}
}

// recordStep appends a step.finished event. It uses its own context so a
// cancelled run still announces how its steps ended.
func (e *Engine) recordStep(r run.Run, st pipeline.Step, res scheduler.StepResult) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	p := events.StepFinishedPayload{Step: st.Name, Validator: st.Validator, Status: string(res.Status), ExitCode: res.ExitCode, DurationMS: res.Duration.Milliseconds()}
	if res.Err != nil {
		p.Error = res.Err.Error()
	}
	if _, err := e.store.AppendEvent(ctx, events.Event{Type: events.StepFinished, Time: e.now().UTC(), RepoID: r.RepoID, ChangeID: r.ChangeID, RunID: r.ID, Payload: p}); err != nil {
		e.log.Error("record step event", "run", r.ID, "step", st.Name, "err", err)
	}
}

func closeLogs(logs map[string]*os.File) {
	for _, f := range logs {
		f.Close()
	}
}

// toSteps converts scheduler results into run steps.
func toSteps(p *pipeline.Pipeline, res scheduler.Result) []run.Step {
	steps := make([]run.Step, len(res.Steps))
	for i, s := range res.Steps {
		steps[i] = run.Step{Name: s.Name, Validator: p.Steps[i].Validator, Status: string(s.Status), ExitCode: s.ExitCode, DurationMS: s.Duration.Milliseconds()}
		if s.Err != nil {
			steps[i].Error = s.Err.Error()
		}
	}
	return steps
}

// Shutdown cancels every run, waits for them to record their state, and
// stops the engine.
func (e *Engine) Shutdown(ctx context.Context) {
	e.stopAll(errShutdown)
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Recover marks runs left running by a previous server process as errored
// and moves their changes to needs_human.
func (e *Engine) Recover(ctx context.Context) error {
	stale, err := e.store.RunningRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range stale {
		e.log.Warn("recovering interrupted run", "run", r.ID, "change", r.ChangeID)
		e.finishErrored(ctx, r, errors.New("interrupted: the Plumbix server stopped during this run"), false)
	}
	return nil
}

// LogPath returns the log file of a step, or "" for invalid names.
func (e *Engine) LogPath(runID, step string) string {
	if !runIDRe.MatchString(runID) || !pipeline.ValidStepName(step) {
		return ""
	}
	return filepath.Join(e.cfg.DataDir, evidenceDir, runID, step, validation.LogFile)
}

// runPipeline performs one iteration of a change. The workspace is removed
// before the caller records the outcome, so a finished run has no leftovers.
func (e *Engine) runPipeline(ctx context.Context, r run.Run, c change.Change) ([]run.Step, *validation.Result, error) {
	work := filepath.Join(e.cfg.DataDir, workDir, r.ID)
	defer os.RemoveAll(work)

	prep, err := e.prepare(ctx, r, c, work)
	if prep.cleanup != nil {
		defer prep.cleanup()
	}
	if err != nil {
		return nil, nil, err
	}
	r.Head, r.Base = prep.head, prep.base

	logs := map[string]*os.File{}
	defer closeLogs(logs)
	opts := e.schedulerOptions(r, prep.workspace, logs)
	opts.Setup = func(st pipeline.Step, finished []scheduler.StepResult) (scheduler.StepSetup, error) {
		if !st.Validator {
			return scheduler.StepSetup{}, nil
		}
		return e.setupValidator(r, prep, work, st, finished)
	}
	res := scheduler.Execute(ctx, prep.pipeline, e.runner, opts)

	steps := toSteps(prep.pipeline, res)
	var results []validation.ValidatorResult
	var infra []string
	for i, s := range res.Steps {
		st := prep.pipeline.Steps[i]
		if st.Validator {
			results = append(results, readVerdict(work, s, c.AcceptanceCriteria))
		} else if s.Status == scheduler.Errored {
			infra = append(infra, fmt.Sprintf("step %s could not run: %v", s.Name, s.Err))
		}
	}
	if ctx.Err() != nil {
		return steps, nil, nil
	}
	combined := validation.Combine(c.AcceptanceCriteria, results, prep.required, prep.changed)
	if len(infra) > 0 && combined.State != change.Rejected {
		// Infrastructure failures make the evidence unreliable; they are
		// never the author's to fix.
		combined.State = change.NeedsHuman
		combined.Reasons = append(infra, combined.Reasons...)
	}
	if combined.State == change.FixRequested && r.Iteration > e.cfg.MaxFixRounds {
		combined.State = change.Escalated
		combined.Reasons = append(combined.Reasons, fmt.Sprintf("fixes were still requested after %d fix rounds", e.cfg.MaxFixRounds))
	}
	return steps, &combined, nil
}

// prepared is everything a run needs before steps start.
type prepared struct {
	head, base, workspace string
	pipeline              *pipeline.Pipeline
	required, changed     []string
	diff                  []byte
	previous              []validation.Finding
	cleanup               func()
}

func (e *Engine) prepare(ctx context.Context, r run.Run, c change.Change, work string) (prepared, error) {
	var p prepared
	repo, err := e.store.GetRepo(ctx, r.RepoID)
	if err != nil {
		return p, fmt.Errorf("load repo: %w", err)
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return p, err
	}
	if p.head, err = m.Head(ctx, c.SourceBranch); err != nil {
		return p, err
	}
	target, err := m.Head(ctx, c.TargetBranch)
	if err != nil {
		return p, err
	}
	if p.base, err = m.MergeBase(ctx, target, p.head); err != nil {
		return p, fmt.Errorf("merge base of %s and %s: %w", c.TargetBranch, c.SourceBranch, err)
	}
	if err := e.store.SetRunCommits(ctx, r.ID, p.head, p.base); err != nil {
		return p, err
	}

	if p.pipeline, err = e.readPipeline(ctx, m, p.head, c.SourceBranch); err != nil {
		return p, err
	}
	if targetSrc, ok, err := m.Show(ctx, target, PipelineFile); err == nil && ok {
		if tp, err := pipeline.Parse(targetSrc); err == nil {
			for _, st := range tp.Steps {
				if st.Validator {
					p.required = append(p.required, st.Name)
				}
			}
		}
	}
	if p.changed, err = m.ChangedFiles(ctx, p.base, p.head); err != nil {
		return p, err
	}
	if p.diff, err = m.Diff(ctx, p.base, p.head); err != nil {
		return p, err
	}
	if p.previous, err = e.previousFindings(ctx, c.ID, r.Iteration); err != nil {
		return p, err
	}

	p.workspace = filepath.Join(work, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return p, err
	}
	remove, err := m.Worktree(ctx, p.head, p.workspace)
	if err != nil {
		return p, err
	}
	p.cleanup = func() { _ = remove() }
	return p, nil
}

// setupValidator writes a validator's inputs and returns its mounts.
func (e *Engine) setupValidator(r run.Run, p prepared, work string, st pipeline.Step, finished []scheduler.StepResult) (scheduler.StepSetup, error) {
	c, err := e.store.GetChange(context.Background(), r.ChangeID)
	if err != nil {
		return scheduler.StepSetup{}, err
	}
	dir := filepath.Join(work, "validators", st.Name)
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	var evidence []validation.StepEvidence
	for _, f := range finished {
		log, _ := os.ReadFile(e.LogPath(r.ID, f.Name))
		evidence = append(evidence, validation.StepEvidence{Step: f.Name, Status: string(f.Status), ExitCode: f.ExitCode, DurationMS: f.Duration.Milliseconds(), Log: log})
	}
	input := validation.NewInput(c, p.base, p.head, p.changed, r.Iteration, p.previous)
	if err := validation.PrepareInputs(in, input, evidence); err != nil {
		return scheduler.StepSetup{}, err
	}
	if err := validation.WriteDiff(in, p.diff); err != nil {
		return scheduler.StepSetup{}, err
	}
	// Validator images run as a non-root user.
	if err := os.MkdirAll(out, 0o755); err != nil {
		return scheduler.StepSetup{}, err
	}
	if err := os.Chmod(out, 0o777); err != nil {
		return scheduler.StepSetup{}, err
	}
	return scheduler.StepSetup{
		WorkspaceReadOnly: true,
		Mounts: []runner.Mount{
			{Source: in, Target: validation.InPath, ReadOnly: true},
			{Source: out, Target: validation.OutPath},
		},
	}, nil
}

func readVerdict(work string, s scheduler.StepResult, criteria []change.Criterion) validation.ValidatorResult {
	vr := validation.ValidatorResult{Name: s.Name}
	if s.Status != scheduler.Succeeded {
		vr.Err = fmt.Errorf("validator step %s", s.Status)
		if s.Err != nil {
			vr.Err = fmt.Errorf("validator step %s: %v", s.Status, s.Err)
		}
		return vr
	}
	v, err := validation.ReadVerdict(filepath.Join(work, "validators", s.Name, "out", validation.VerdictFile), criteria)
	if err != nil {
		vr.Err = err
		return vr
	}
	vr.Verdict = &v
	return vr
}

// previousFindings returns the findings of the previous iteration, if any.
func (e *Engine) previousFindings(ctx context.Context, changeID string, iteration int) ([]validation.Finding, error) {
	runs, err := e.store.ListRuns(ctx, changeID)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.Iteration == iteration-1 && r.Result != nil {
			out := make([]validation.Finding, 0, len(r.Result.Findings))
			for _, f := range r.Result.Findings {
				out = append(out, f.Finding)
			}
			return out, nil
		}
	}
	return nil, nil
}

// end records the run's outcome and moves the change. A cancelled run is
// always treated as cancelled, whatever error the cancellation caused: a run
// replaced by a newer one leaves the change alone, and a run stopped by
// shutdown is recorded as interrupted.
func (e *Engine) end(ctx context.Context, r run.Run, steps []run.Step, result *validation.Result, runErr error) {
	wctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errShutdown) {
			r.Steps = steps
			e.finishErrored(wctx, r, errShutdown, true)
			return
		}
		if err := e.store.FinishRun(wctx, r.ID, run.Finish{Status: run.Cancelled, Error: context.Cause(ctx).Error(), Steps: steps, At: e.now().UTC()}); err != nil {
			e.log.Error("record cancelled run", "run", r.ID, "err", err)
		}
		return
	}
	if runErr != nil {
		r.Steps = steps
		e.finishErrored(wctx, r, runErr, true)
		return
	}
	if result == nil {
		// A plain branch run: no verdict, no change to move.
		if err := e.store.FinishRun(wctx, r.ID, run.Finish{Status: run.Finished, Steps: steps, At: e.now().UTC()}); err != nil {
			e.log.Error("record run", "run", r.ID, "err", err)
		}
		return
	}
	if err := e.store.FinishRun(wctx, r.ID, run.Finish{Status: run.Finished, Outcome: result.State, Result: result, Steps: steps, At: e.now().UTC()}); err != nil {
		e.log.Error("record run", "run", r.ID, "err", err)
		return
	}
	e.moveChange(wctx, r, result.State, true)
}

// finishErrored records an errored run and moves its change to needs_human.
// requireActive is false only for recovery, where no run is active.
func (e *Engine) finishErrored(ctx context.Context, r run.Run, runErr error, requireActive bool) {
	var outcome change.State
	if r.ChangeID != "" {
		outcome = change.NeedsHuman
	}
	if err := e.store.FinishRun(ctx, r.ID, run.Finish{Status: run.Errored, Outcome: outcome, Error: runErr.Error(), Steps: r.Steps, At: e.now().UTC()}); err != nil {
		e.log.Error("record errored run", "run", r.ID, "err", err)
	}
	e.moveChange(ctx, r, change.NeedsHuman, requireActive)
}

// moveChange sets the change's state if it is still running and, when
// requireActive is set, only if r is still the change's active run. The
// check and the update happen under the lock Validate holds while it
// replaces a run, so a superseded run can never overwrite a newer one.
func (e *Engine) moveChange(ctx context.Context, r run.Run, to change.State, requireActive bool) {
	if r.ChangeID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if requireActive {
		if a, ok := e.active[r.ChangeID]; !ok || a.runID != r.ID {
			return
		}
	}
	err := e.store.SetChangeState(ctx, r.ChangeID, change.Running, to, e.now().UTC())
	if err != nil && !errors.Is(err, change.ErrConflict) {
		e.log.Error("update change state", "change", r.ChangeID, "err", err)
	}
}

func (e *Engine) openLog(runID, step string) (*os.File, error) {
	path := e.LogPath(runID, step)
	if path == "" {
		return nil, fmt.Errorf("invalid run or step name")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

func (e *Engine) repoLock(repoID string) *sync.Mutex {
	l, _ := e.mirrorLock.LoadOrStore(repoID, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
