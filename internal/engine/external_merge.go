package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/guard"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// ErrGitHubMerges means the repository lets GitHub merge its pull
// requests, so Plumbix does not push this change (WU-054).
var ErrGitHubMerges = errors.New("GitHub merges this repository's pull requests")

// ExternalMerge describes a merge GitHub made of a pull request.
type ExternalMerge struct {
	Commit   string // the commit at the target's head after the merge
	HeadSHA  string // the pull request's head that was merged
	Commits  int    // how many commits the pull request had
	MergedBy string // the GitHub login that merged
	// AutoMerge: GitHub merged on MergedBy's behalf once the gate was green
	// (WU-063); MergedBy requested, nobody pressed.
	AutoMerge bool
}

// RecordExternalMerge records that GitHub merged the change's pull request
// (WU-054): the merge record names GitHub's merge commit and the target's
// head before it, and the change moves to merged from whatever state it
// was in. The merge happened whether or not the gate agreed; the caller
// decides whether that deserves an alert. The merged pipeline files are
// recorded as approved only when the gate had agreed on the merged head
// (the change was approved, by the validators or by a person, at that
// head): a merge past the gate must not launder a pipeline file into one
// that gets secrets (A38), so it raises pipeline_unapproved instead when a
// pipeline file is among what landed.
func (e *Engine) RecordExternalMerge(ctx context.Context, changeID string, m ExternalMerge) (change.Change, error) {
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return change.Change{}, fmt.Errorf("%w: change %s", change.ErrNotFound, changeID)
		}
		return change.Change{}, err
	}
	if c.State.Final() {
		return change.Change{}, fmt.Errorf("%w: change %s is already %s", change.ErrInvalidState, changeID, c.State)
	}
	repo, err := e.store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return change.Change{}, fmt.Errorf("load repo: %w", err)
	}
	mirror, err := e.fetch(ctx, repo)
	if err != nil {
		return change.Change{}, err
	}
	previous, err := e.previousHead(ctx, mirror, m)
	if err != nil {
		return change.Change{}, err
	}
	runs, err := e.store.ListRuns(ctx, c.ID)
	if err != nil {
		return change.Change{}, err
	}
	runID := ""
	if len(runs) > 0 {
		runID = runs[len(runs)-1].ID
	}
	now := e.now().UTC()
	rec := change.MergeRecord{ChangeID: c.ID, RepoID: c.RepoID, Branch: c.TargetBranch,
		Merge: change.Merge{Commit: m.Commit, Previous: previous, Strategy: change.StrategyGitHub, RunID: runID, MergedBy: m.MergedBy, AutoMerge: m.AutoMerge, MergedAt: now}}
	if err := e.store.RecordMerge(ctx, rec); err != nil {
		return change.Change{}, fmt.Errorf("record GitHub's merge of %s: %w", short(m.Commit), err)
	}
	switch why := approvalCovers(c, runs, m.HeadSHA); {
	case why != "":
		// Not the gate's merge: only alert when it brought a pipeline
		// file, which is what the approval record protects.
		e.log.Warn("merged on GitHub past the gate; its pipeline files are not recorded as approved", "change", c.ID, "state", c.State, "why", why)
		if touched, err := e.touchesPipeline(ctx, mirror, previous, m.Commit); err != nil || touched {
			e.raiseUnrecorded(ctx, repo, c.ID, c.TargetBranch)
		}
	default:
		if err := e.approveMerged(ctx, mirror, repo, c.ID, previous, m.Commit); err != nil {
			e.log.Error("merged on GitHub, but the pipeline files were not recorded as approved", "change", c.ID, "err", err)
			e.raiseUnrecorded(ctx, repo, c.ID, c.TargetBranch)
		}
	}
	// The change may move while this runs (a run ending); try from the
	// state seen, then once more from the current one.
	for attempt := 0; attempt < 2; attempt++ {
		err = e.store.SetChangeState(ctx, c.ID, c.State, change.Merged, now)
		if err == nil || !errors.Is(err, change.ErrConflict) {
			break
		}
		if c, err = e.store.GetChange(ctx, c.ID); err != nil || c.State.Final() {
			break
		}
	}
	if err != nil {
		e.log.Warn("change state after GitHub's merge", "change", c.ID, "err", err)
	}
	e.log.Info("merged on GitHub", "change", c.ID, "branch", c.TargetBranch, "commit", m.Commit, "by", m.MergedBy)
	return e.store.GetChange(ctx, c.ID)
}

// approvalCovers says why the change's approval does not cover the merged
// head: the change was not approved, or the approval (the validators' on
// the latest finished run, or a person's) stands for another head and the
// merged one was never judged. Empty when it covers it.
func approvalCovers(c change.Change, runs []run.Run, head string) string {
	if c.State != change.Approved {
		return "the change is " + string(c.State) + ", not approved"
	}
	approved := ""
	if c.HumanApproval != nil {
		approved = c.HumanApproval.Head
	} else {
		for i := len(runs) - 1; i >= 0; i-- {
			if runs[i].Status == run.Finished && runs[i].Head != "" {
				approved = runs[i].Head
				break
			}
		}
	}
	if head == "" || approved == "" || approved != head {
		return fmt.Sprintf("the approval stands for %s, GitHub merged %s", short(approved), short(head))
	}
	return ""
}

// touchesPipeline reports whether the commits between previous and commit
// change a pipeline file.
func (e *Engine) touchesPipeline(ctx context.Context, m *gitops.Mirror, previous, commit string) (bool, error) {
	if previous == "" {
		return true, nil
	}
	changed, err := m.ChangedFiles(ctx, previous, commit)
	if err != nil {
		return false, err
	}
	for _, p := range changed {
		if guard.IsPipelineFile(p) {
			return true, nil
		}
	}
	return false, nil
}

// previousHead works out the target's head before GitHub's merge commit:
// the first parent of a merge commit; for a one-parent commit, the commit
// below the rebased pull-request commits when the commits on the target
// match the pull request's by subject and author (a rebase merge), else
// the commit's parent (a squash). A root commit has no previous head.
func (e *Engine) previousHead(ctx context.Context, m *gitops.Mirror, em ExternalMerge) (string, error) {
	top, err := m.Commit(ctx, em.Commit)
	if err != nil {
		return "", fmt.Errorf("GitHub's merge commit %s is not on the remote: %w", short(em.Commit), err)
	}
	switch {
	case len(top.Parents) == 0:
		return "", nil
	case len(top.Parents) > 1:
		return top.Parents[0], nil
	}
	if em.Commits > 1 && em.HeadSHA != "" {
		if prev, ok := e.rebasedPrevious(ctx, m, em); ok {
			return prev, nil
		}
	}
	return top.Parents[0], nil
}

// rebasedPrevious reports the head below a rebase merge's commits when the
// last Commits commits on the target match the pull request's.
func (e *Engine) rebasedPrevious(ctx context.Context, m *gitops.Mirror, em ExternalMerge) (string, bool) {
	base, err := m.MergeBase(ctx, em.Commit, em.HeadSHA)
	if err != nil {
		return "", false
	}
	prCommits, err := m.Commits(ctx, base, em.HeadSHA)
	if err != nil || len(prCommits) != em.Commits {
		return "", false
	}
	landed, err := m.Commits(ctx, fmt.Sprintf("%s~%d", em.Commit, em.Commits), em.Commit)
	if err != nil || len(landed) != len(prCommits) {
		return "", false
	}
	for i := range landed {
		a, b := landed[i], prCommits[i]
		if a.Subject != b.Subject || a.Author != b.Author || a.Email != b.Email {
			return "", false
		}
	}
	last := landed[len(landed)-1]
	if len(last.Parents) != 1 {
		return "", false
	}
	return last.Parents[0], true
}
