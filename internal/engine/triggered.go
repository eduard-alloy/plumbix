package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/scheduler"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// PipelinesDir holds the triggered pipelines, one file each (WU-045, A36).
const PipelinesDir = ".plumbix/pipelines"

var (
	// ErrPipelineDisabled refuses to run a pipeline that is or holds a disabled step.
	ErrPipelineDisabled = errors.New("pipeline is disabled")
	// ErrNotManual refuses a manual run of a pipeline without on.manual.
	ErrNotManual = errors.New("pipeline cannot be run by hand: it has no on.manual")
)

// TriggeredFile is one file under .plumbix/pipelines/: its parsed pipeline,
// or the problems that keep it from parsing.
type TriggeredFile struct {
	Name     string
	File     string
	Pipeline *pipeline.Pipeline
	Problems []pipeline.Error
}

// TriggeredView lists a branch head's triggered pipelines.
type TriggeredView struct {
	Branch    string
	SHA       string
	Pipelines []TriggeredFile
}

// TriggeredPipelines reads the triggered pipelines at a branch head.
func (e *Engine) TriggeredPipelines(ctx context.Context, repoID, branch string) (TriggeredView, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return TriggeredView{}, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return TriggeredView{}, err
	}
	head, err := m.Head(ctx, branch)
	if err != nil {
		return TriggeredView{}, err
	}
	files, err := triggeredAt(ctx, m, head)
	if err != nil {
		return TriggeredView{}, err
	}
	return TriggeredView{Branch: branch, SHA: head, Pipelines: files}, nil
}

// triggeredAt reads every .yaml file directly under PipelinesDir at commit.
func triggeredAt(ctx context.Context, m *gitops.Mirror, commit string) ([]TriggeredFile, error) {
	paths, err := m.ListFiles(ctx, commit, PipelinesDir)
	if err != nil {
		return nil, err
	}
	var out []TriggeredFile
	for _, p := range paths {
		name, ok := strings.CutSuffix(path.Base(p), ".yaml")
		if !ok {
			continue
		}
		f := TriggeredFile{Name: name, File: p}
		if !pipeline.ValidStepName(name) {
			f.Problems = []pipeline.Error{{Message: "the file name must be lowercase letters, digits and dashes, at most 63 characters, then .yaml"}}
			out = append(out, f)
			continue
		}
		src, _, err := m.Show(ctx, commit, p)
		if err != nil {
			return nil, err
		}
		pl, err := pipeline.ParseTriggered(src)
		var ve *pipeline.ValidationError
		switch {
		case err == nil:
			f.Pipeline = pl
		case errors.As(err, &ve):
			f.Problems = ve.Errors
		default:
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// RunPushed starts, for each branch move of a repository seen in one poll,
// every enabled triggered pipeline whose push trigger matches. The mirror
// is fetched once for all moves. Each run is pinned to its move's After.
// Disabled and invalid pipelines start nothing and are logged. A move that
// fails does not stop the others; the errors are joined.
func (e *Engine) RunPushed(ctx context.Context, repoID string, moves []run.Move) ([]run.Run, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return nil, err
	}
	var runs []run.Run
	var errs []error
	for _, mv := range moves {
		started, err := e.runMove(ctx, m, repo, mv)
		runs = append(runs, started...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mv.Branch, err))
		}
	}
	return runs, errors.Join(errs...)
}

// runMove starts the pipelines one branch move matches.
func (e *Engine) runMove(ctx context.Context, m *gitops.Mirror, repo change.Repo, mv run.Move) ([]run.Run, error) {
	files, err := triggeredAt(ctx, m, mv.After)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	var changed []string
	if mv.Before != "" {
		if changed, err = m.ChangedFiles(ctx, mv.Before, mv.After); err != nil {
			return nil, err
		}
	}
	var runs []run.Run
	var errs []error
	for _, f := range files {
		switch {
		case f.Pipeline == nil:
			e.log.Warn("triggered pipeline does not parse; not run", "repo", repo.ID, "file", f.File, "problems", len(f.Problems))
		case f.Pipeline.On.Push == nil || !f.Pipeline.On.Push.Matches(mv.Branch, changed, mv.Before == ""):
		case f.Pipeline.Disabled():
			e.log.Info("triggered pipeline is disabled; not run", "repo", repo.ID, "pipeline", f.Name, "blockers", f.Pipeline.BlockerSummary())
		default:
			r, err := e.startTriggered(ctx, repo, f, mv.Branch, mv.After)
			if err != nil {
				errs = append(errs, fmt.Errorf("pipeline %s: %w", f.Name, err))
				continue
			}
			runs = append(runs, r)
		}
	}
	return runs, errors.Join(errs...)
}

// TriggeredAt reads the triggered pipelines at a commit the mirror already
// has, without fetching: for callers that just fetched, such as the Images
// page after PipelineFile.
func (e *Engine) TriggeredAt(ctx context.Context, repoID, commit string) ([]TriggeredFile, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	m, err := e.mirror(repo)
	if err != nil {
		return nil, err
	}
	return triggeredAt(ctx, m, commit)
}

// RunPipeline runs a triggered pipeline by hand at a branch head.
func (e *Engine) RunPipeline(ctx context.Context, repoID, name, branch string) (run.Run, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return run.Run{}, err
	}
	if !pipeline.ValidStepName(name) {
		return run.Run{}, fmt.Errorf("%w: pipeline %s", change.ErrNotFound, name)
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return run.Run{}, err
	}
	head, err := m.Head(ctx, branch)
	if err != nil {
		return run.Run{}, err
	}
	file := PipelinesDir + "/" + name + ".yaml"
	src, ok, err := m.Show(ctx, head, file)
	if err != nil {
		return run.Run{}, err
	}
	if !ok {
		return run.Run{}, fmt.Errorf("%w: pipeline %s on %s", change.ErrNotFound, name, branch)
	}
	p, err := pipeline.ParseTriggered(src)
	if err != nil {
		return run.Run{}, err
	}
	switch {
	case !p.On.Manual:
		return run.Run{}, ErrNotManual
	case p.Disabled():
		return run.Run{}, fmt.Errorf("%w: steps disabled by reason %s", ErrPipelineDisabled, p.BlockerSummary())
	}
	return e.startTriggered(ctx, repo, TriggeredFile{Name: name, File: file, Pipeline: p}, branch, head)
}

// startTriggered records and launches one triggered pipeline's run at sha;
// a newer run of the same pipeline on the same branch supersedes it.
func (e *Engine) startTriggered(ctx context.Context, repo change.Repo, f TriggeredFile, branch, sha string) (run.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := "pipeline:" + repo.ID + ":" + branch + ":" + f.Name
	if prev, ok := e.active[key]; ok {
		prev.cancel(errSuperseded)
	}
	r := run.Run{ID: change.NewID("run_"), RepoID: repo.ID, Branch: branch, Pipeline: f.Name, Head: sha, Status: run.Running, StartedAt: e.now().UTC(), Steps: []run.Step{}}
	if err := e.store.CreateRun(ctx, r); err != nil {
		return run.Run{}, err
	}
	p := f.Pipeline
	e.launch(key, r, func(ctx context.Context) ([]run.Step, *validation.Result, error) {
		return e.runTriggered(ctx, r, repo, p, sha)
	})
	return r, nil
}

// runTriggered runs a triggered pipeline in a worktree of sha.
func (e *Engine) runTriggered(ctx context.Context, r run.Run, repo change.Repo, p *pipeline.Pipeline, sha string) ([]run.Step, *validation.Result, error) {
	work := filepath.Join(e.cfg.DataDir, workDir, r.ID)
	defer os.RemoveAll(work)
	if err := e.store.SetRunCommits(ctx, r.ID, sha, ""); err != nil {
		return nil, nil, err
	}
	m, err := e.mirror(repo)
	if err != nil {
		return nil, nil, err
	}
	workspace := filepath.Join(work, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, nil, err
	}
	remove, err := m.Worktree(ctx, sha, workspace)
	if err != nil {
		return nil, nil, err
	}
	defer remove()
	logs := map[string]*os.File{}
	defer closeLogs(logs)
	res := scheduler.Execute(ctx, p, e.runner, e.schedulerOptions(r, repo, r.Branch, workspace, logs))
	return toSteps(p, res), nil, nil
}
