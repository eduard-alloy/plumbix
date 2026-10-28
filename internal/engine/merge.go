package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// Merge refusals. Both mean the validated tree is no longer what would land.
var (
	ErrSourceMoved = errors.New("source branch moved since validation")
	ErrTargetMoved = errors.New("target branch moved since validation")
)

// Merge lands an approved change on its target branch and moves it to
// merged. The target receives exactly the tree the validators judged: the
// source must still point at the validated commit, and the target must be
// the run's base, which means the source already contains it. While the
// merge runs, no new validation can start for the change.
func (e *Engine) Merge(ctx context.Context, changeID string) (change.Change, error) {
	c, r, err := e.beginMerge(ctx, changeID)
	if err != nil {
		return change.Change{}, err
	}
	defer e.endMerge(changeID)

	repo, err := e.store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return change.Change{}, fmt.Errorf("load repo: %w", err)
	}
	if repo.Merger == change.MergerGitHub && c.PullRequest != nil {
		return change.Change{}, fmt.Errorf("%w: merge pull request #%d on GitHub and Plumbix records it", ErrGitHubMerges, c.PullRequest.Number)
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return change.Change{}, err
	}
	head, err := m.Head(ctx, c.SourceBranch)
	if err != nil {
		return change.Change{}, err
	}
	if head != r.Head {
		return change.Change{}, fmt.Errorf("%w: %s is at %s, validated %s; the new commits need a verdict first", ErrSourceMoved, c.SourceBranch, short(head), short(r.Head))
	}
	target, err := m.Head(ctx, c.TargetBranch)
	if err != nil {
		return change.Change{}, err
	}
	if target != r.Base {
		return change.Change{}, fmt.Errorf("%w: %s is at %s, validation assumed %s; bring %s into %s, push, and merge after the new verdict", ErrTargetMoved, c.TargetBranch, short(target), short(r.Base), c.TargetBranch, c.SourceBranch)
	}

	message := fmt.Sprintf("%s\n\n%s\n\nPlumbix-Change: %s\nPlumbix-Run: %s\n", c.Title, c.Intent, c.ID, r.ID)
	var sha string
	switch repo.MergeStrategy {
	case "squash":
		sha, err = m.Squash(ctx, head, target, message)
	case "merge":
		sha, err = m.MergeCommit(ctx, head, target, message)
	case "rebase":
		sha = head // the validated commits already sit on top of the target
	default:
		err = fmt.Errorf("unknown merge strategy %q", repo.MergeStrategy)
	}
	if err != nil {
		return change.Change{}, err
	}
	if e.beforePush != nil {
		e.beforePush()
	}
	if err := m.Push(ctx, c.TargetBranch, sha, target); err != nil {
		if errors.Is(err, gitops.ErrPushDenied) {
			return change.Change{}, fmt.Errorf("%w; set a token for repository %s in Settings and merge again", err, repo.ID)
		}
		return change.Change{}, err
	}

	now := e.now().UTC()
	rec := change.MergeRecord{ChangeID: c.ID, RepoID: c.RepoID, Branch: c.TargetBranch,
		Merge: change.Merge{Commit: sha, Previous: target, Strategy: repo.MergeStrategy, RunID: r.ID, MergedAt: now}}
	if err := e.store.RecordMerge(ctx, rec); err != nil {
		return change.Change{}, fmt.Errorf("pushed %s to %s but could not record the merge: %w", short(sha), c.TargetBranch, err)
	}
	if err := e.approveMerged(ctx, m, repo, c.ID, target, sha); err != nil {
		// The merge is on the host; without the record, runs of the new
		// pipeline files on the target get no secrets until approved again.
		e.log.Error("merged, but the pipeline files were not recorded as approved", "change", c.ID, "err", err)
		e.raiseUnrecorded(ctx, repo, c.ID, c.TargetBranch)
	}
	if err := e.store.SetChangeState(ctx, c.ID, change.Approved, change.Merged, now); err != nil {
		// The change was closed while the merge ran. The code is on the
		// target and the merge record says so; only the state disagrees.
		e.log.Warn("change left approved state during merge", "change", c.ID, "err", err)
	}
	e.log.Info("merged change", "change", c.ID, "branch", c.TargetBranch, "commit", sha, "strategy", repo.MergeStrategy)
	return e.store.GetChange(ctx, c.ID)
}

// beginMerge checks that the change can merge, marks it as merging so no
// validation starts meanwhile, and returns the approved run.
func (e *Engine) beginMerge(ctx context.Context, changeID string) (change.Change, run.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return c, run.Run{}, fmt.Errorf("%w: change %s", change.ErrNotFound, changeID)
		}
		return c, run.Run{}, err
	}
	if c.State != change.Approved {
		return c, run.Run{}, fmt.Errorf("%w: change %s is %s, only approved changes merge", change.ErrInvalidState, changeID, c.State)
	}
	if e.merging[changeID] {
		return c, run.Run{}, fmt.Errorf("%w: change %s is being merged", change.ErrInvalidState, changeID)
	}
	runs, err := e.store.ListRuns(ctx, changeID)
	if err != nil {
		return c, run.Run{}, err
	}
	if len(runs) == 0 {
		return c, run.Run{}, fmt.Errorf("%w: change %s has no run", change.ErrInvalidState, changeID)
	}
	last := runs[len(runs)-1]
	byPerson := c.HumanApproval != nil && c.HumanApproval.Head == last.Head
	if last.Status != run.Finished || last.Head == "" || (last.Outcome != change.Approved && !byPerson) {
		return c, run.Run{}, fmt.Errorf("%w: the latest run of change %s is not an approval", change.ErrInvalidState, changeID)
	}
	if e.merging == nil {
		e.merging = map[string]bool{}
	}
	e.merging[changeID] = true
	return c, last, nil
}

func (e *Engine) endMerge(changeID string) {
	e.mu.Lock()
	delete(e.merging, changeID)
	e.mu.Unlock()
}
