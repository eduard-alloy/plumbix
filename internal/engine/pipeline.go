package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// PipelineFileView is a repository's pipeline file at a branch head.
type PipelineFileView struct {
	Branch   string
	SHA      string
	Content  []byte
	Found    bool
	Pipeline *pipeline.Pipeline // nil when the file is missing or invalid
	Problems []pipeline.Error   // why Pipeline is nil, when it is
}

// PipelineFile fetches the repository and reads its pipeline file at the
// branch head. A missing or invalid file is a view with problems, not an
// error: the editor shows it and offers to fix it.
func (e *Engine) PipelineFile(ctx context.Context, repoID, branch string) (PipelineFileView, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return PipelineFileView{}, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return PipelineFileView{}, err
	}
	head, err := m.Head(ctx, branch)
	if err != nil {
		return PipelineFileView{}, err
	}
	v := PipelineFileView{Branch: branch, SHA: head}
	src, ok, err := m.Show(ctx, head, PipelineFile)
	if err != nil {
		return PipelineFileView{}, err
	}
	if !ok {
		v.Problems = []pipeline.Error{{Message: fmt.Sprintf("no %s on %s", PipelineFile, branch)}}
		return v, nil
	}
	v.Found, v.Content = true, src
	p, err := pipeline.Parse(src)
	var ve *pipeline.ValidationError
	switch {
	case err == nil:
		v.Pipeline = p
	case errors.As(err, &ve):
		v.Problems = ve.Errors
	default:
		return PipelineFileView{}, err
	}
	return v, nil
}

// Proposal is a file change Plumbix commits and pushes as a new branch.
type Proposal struct {
	TargetBranch string // the branch whose head the commit builds on
	Branch       string // the new branch; must not exist on the host
	Path         string
	Content      []byte
	Message      string
}

// ProposeFile commits one file on the target branch's head and pushes it as
// a new branch, with the repository's credentials. It never touches the
// target branch. The caller submits the change.
func (e *Engine) ProposeFile(ctx context.Context, repoID string, p Proposal) (string, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return "", err
	}
	if !change.ValidBranch(p.Branch) {
		return "", &change.InvalidError{Problems: []change.Problem{{Field: "branch", Message: "is not a valid branch name"}}}
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return "", err
	}
	base, err := m.Head(ctx, p.TargetBranch)
	if err != nil {
		return "", err
	}
	if _, err := m.Head(ctx, p.Branch); err == nil {
		return "", fmt.Errorf("%w: %s", gitops.ErrBranchExists, p.Branch)
	}
	sha, err := m.CommitFile(ctx, base, p.Path, p.Content, p.Message)
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", p.Path, err)
	}
	if err := m.PushBranch(ctx, p.Branch, sha); err != nil {
		if errors.Is(err, gitops.ErrPushDenied) {
			return "", fmt.Errorf("%w; set a token for repository %s in Settings and try again", err, repo.ID)
		}
		return "", err
	}
	e.log.Info("proposed file", "repo", repo.ID, "branch", p.Branch, "path", p.Path, "commit", sha)
	return sha, nil
}
