package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// Errors from refreshing a branch.
var (
	// ErrAlreadyCurrent means the branch already contains the target's head.
	ErrAlreadyCurrent = errors.New("branch already contains the target")
	// ErrNotPlumbixBranch means the branch is not one Plumbix created, or
	// carries a commit Plumbix did not make, so Plumbix leaves it alone.
	ErrNotPlumbixBranch = errors.New("not a branch Plumbix created")
)

// Refresh brings a change's target branch into its source branch, for
// branches Plumbix created (the pipeline editor's): it merges the target's
// head into the branch with Plumbix as author, pushes the merge commit
// under a lease on the old head, records change.refreshed, and starts a new
// iteration. A person asks for it, so it runs even on a sticky state; the
// new run ends any standing approval, as every run does. Branches with a
// commit by anyone else are refused: those belong to their authors.
func (e *Engine) Refresh(ctx context.Context, changeID string) (change.Change, error) {
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return change.Change{}, fmt.Errorf("%w: change %s", change.ErrNotFound, changeID)
		}
		return change.Change{}, err
	}
	if c.State.Final() || c.State == change.Running {
		return change.Change{}, fmt.Errorf("%w: change %s is %s", change.ErrInvalidState, changeID, c.State)
	}
	if !c.PlumbixBranch {
		return change.Change{}, fmt.Errorf("%w: %s was pushed by its author, who brings %s in", ErrNotPlumbixBranch, c.SourceBranch, c.TargetBranch)
	}
	repo, err := e.store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return change.Change{}, fmt.Errorf("load repo: %w", err)
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return change.Change{}, err
	}
	head, err := m.Head(ctx, c.SourceBranch)
	if err != nil {
		return change.Change{}, err
	}
	target, err := m.Head(ctx, c.TargetBranch)
	if err != nil {
		return change.Change{}, err
	}
	base, err := m.MergeBase(ctx, target, head)
	if err != nil {
		return change.Change{}, err
	}
	if base == target {
		return change.Change{}, fmt.Errorf("%w: %s already contains %s at %s", ErrAlreadyCurrent, c.SourceBranch, c.TargetBranch, short(target))
	}
	commits, err := m.Commits(ctx, base, head)
	if err != nil {
		return change.Change{}, err
	}
	for _, cm := range commits {
		if cm.Author != gitops.CommitterName || cm.Email != gitops.CommitterEmail {
			return change.Change{}, fmt.Errorf("%w: commit %s on %s is by %s, who brings %s in", ErrNotPlumbixBranch, short(cm.SHA), c.SourceBranch, cm.Author, c.TargetBranch)
		}
	}
	message := fmt.Sprintf("Merge %s into %s\n\nPlumbix-Change: %s\n", c.TargetBranch, c.SourceBranch, c.ID)
	sha, err := m.MergeBranches(ctx, head, target, message)
	if err != nil {
		return change.Change{}, err
	}
	if err := m.Push(ctx, c.SourceBranch, sha, head); err != nil {
		if errors.Is(err, gitops.ErrPushDenied) {
			return change.Change{}, fmt.Errorf("%w; set a token for repository %s in Settings and try again", err, repo.ID)
		}
		return change.Change{}, err
	}
	if _, err := e.store.AppendEvent(ctx, events.Event{Type: events.ChangeRefreshed, Time: e.now().UTC(), RepoID: c.RepoID, ChangeID: c.ID,
		Payload: events.ChangeRefreshedPayload{FromHead: head, Head: sha, TargetHead: target}}); err != nil {
		return change.Change{}, fmt.Errorf("pushed %s to %s but could not record the refresh: %w", short(sha), c.SourceBranch, err)
	}
	e.log.Info("refreshed branch from target", "change", c.ID, "branch", c.SourceBranch, "commit", short(sha), "target", short(target))
	if _, err := e.Validate(ctx, c.ID); err != nil {
		// The poller may have seen the push first and started the run itself.
		if fresh, gerr := e.store.GetChange(ctx, c.ID); gerr == nil && fresh.State == change.Running {
			return fresh, nil
		}
		return change.Change{}, err
	}
	return e.store.GetChange(ctx, c.ID)
}
