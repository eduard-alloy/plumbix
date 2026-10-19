package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/guard"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// guardReview runs the pipeline guard on the pipeline files a change
// touches, comparing base and head (WU-051).
func (e *Engine) guardReview(ctx context.Context, m *gitops.Mirror, repo change.Repo, base, head string, changed []string) ([]validation.Risk, error) {
	var files []guard.File
	for _, p := range changed {
		if !guard.IsPipelineFile(p) {
			continue
		}
		f := guard.File{Path: p}
		for _, side := range []struct {
			commit string
			into   *[]byte
		}{{base, &f.Before}, {head, &f.After}} {
			src, ok, err := m.Show(ctx, side.commit, p)
			if err != nil {
				return nil, err
			}
			if ok {
				*side.into = src
			}
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, nil
	}
	known := map[string]bool{}
	for k := range e.secrets() {
		known[k] = true
	}
	return guard.Review(guard.Input{Files: files, KnownSecrets: known, Protected: slices.Clone(repo.ProtectedBranches)}), nil
}

// sameValidator reports whether a change's validator step is the one the
// target branch defines: everything that decides what runs with the key.
// needs, stage and line may differ.
func sameValidator(target, st pipeline.Step) bool {
	return target.Image == st.Image && target.Run == st.Run && slices.Equal(target.Commands, st.Commands) &&
		maps.Equal(target.Env, st.Env) && slices.Equal(target.Secrets, st.Secrets) && target.Reviewer == st.Reviewer &&
		target.External == st.External && target.Effort == st.Effort && target.Advisory == st.Advisory
}

// trustFirstUse approves every pipeline file at commit, a protected
// branch's head, when the repository has no approvals at all: the files that
// were there before WU-051 keep their secrets (logged).
func (e *Engine) trustFirstUse(ctx context.Context, m *gitops.Mirror, repo change.Repo, commit string) error {
	has, err := e.store.HasPipelineApprovals(ctx, repo.ID)
	if err != nil || has {
		return err
	}
	paths := []string{PipelineFile}
	triggered, err := m.ListFiles(ctx, commit, PipelinesDir)
	if err != nil {
		return err
	}
	paths = append(paths, triggered...)
	var files []guard.Approval
	for _, p := range paths {
		if !guard.IsPipelineFile(p) {
			continue
		}
		src, ok, err := m.Show(ctx, commit, p)
		if err != nil {
			return err
		}
		if ok {
			files = append(files, guard.Approval{Path: p, Digest: guard.Digest(src)})
		}
	}
	if len(files) == 0 {
		return nil
	}
	e.log.Warn("pipeline files trusted on first use: the repository had no approved pipeline files", "repo", repo.ID, "commit", short(commit), "files", len(files))
	return e.store.ApprovePipelines(ctx, repo.ID, files, "", e.now().UTC())
}

// approveMerged records, as approved by the change, the content of every
// pipeline file the merge changed (WU-051). Files the merge did not touch
// are left alone, so an edit pushed past Plumbix is never approved by an
// unrelated merge.
func (e *Engine) approveMerged(ctx context.Context, m *gitops.Mirror, repo change.Repo, changeID, previous, merged string) error {
	// The target as it was before this merge is what was there before
	// WU-051 when nothing is approved yet.
	if err := e.trustFirstUse(ctx, m, repo, previous); err != nil {
		return err
	}
	changed, err := m.ChangedFiles(ctx, previous, merged)
	if err != nil {
		return err
	}
	var files []guard.Approval
	for _, p := range changed {
		if !guard.IsPipelineFile(p) {
			continue
		}
		src, ok, err := m.Show(ctx, merged, p)
		if err != nil {
			return err
		}
		if ok {
			files = append(files, guard.Approval{Path: p, Digest: guard.Digest(src)})
		}
	}
	if len(files) == 0 {
		return nil
	}
	if err := e.store.ApprovePipelines(ctx, repo.ID, files, changeID, e.now().UTC()); err != nil {
		return fmt.Errorf("record approved pipeline files: %w", err)
	}
	return nil
}

// raiseUnrecorded tells a person that a merge's pipeline files were not
// recorded as approved, so their runs on the target will get no secrets.
func (e *Engine) raiseUnrecorded(ctx context.Context, repo change.Repo, changeID, branch string, cause error) {
	a := alert.Alert{Kind: alert.PipelineUnapproved, Key: repo.ID + ":merge:" + changeID, RepoID: repo.ID, CreatedAt: e.now().UTC(),
		Message: fmt.Sprintf("Change %s merged into %s, but its pipeline files were not recorded as approved (%v): their runs on %s get no secrets until they are approved again.", changeID, branch, cause, branch),
		Details: map[string]any{"change_id": changeID, "branch": branch}}
	if _, _, err := e.store.Raise(ctx, a); err != nil {
		e.log.Warn("raise pipeline_unapproved alert", "repo", repo.ID, "change", changeID, "err", err)
	}
}
