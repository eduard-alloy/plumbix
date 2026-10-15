// Package gitops runs git operations on bare mirrors of registered repos
// through the system git command.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUnknownBranch means the branch doesn't exist in the mirror.
var ErrUnknownBranch = errors.New("unknown branch")

// Mirror is a bare mirror of one remote repository.
type Mirror struct {
	dir string
	url string
}

// Open returns the mirror at dir for the remote url. Nothing is created
// until Fetch.
func Open(dir, url string) *Mirror { return &Mirror{dir: dir, url: url} }

// git runs a git command in the mirror and returns stdout.
func (m *Mirror) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", m.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Fetch creates the mirror if needed and updates every branch from the
// remote, removing branches the remote no longer has.
func (m *Mirror) Fetch(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(m.dir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(m.dir, 0o755); err != nil {
			return fmt.Errorf("create mirror: %w", err)
		}
		if _, err := m.git(ctx, "init", "-q", "--bare"); err != nil {
			return err
		}
	}
	_, err := m.git(ctx, "fetch", "-q", "--prune", "--force", "--no-tags", m.url, "+refs/heads/*:refs/heads/*")
	return err
}

// Head returns the commit a branch points to.
func (m *Mirror) Head(ctx context.Context, branch string) (string, error) {
	out, err := m.git(ctx, "rev-parse", "--verify", "-q", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownBranch, branch)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the best common ancestor of two commits.
func (m *Mirror) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := m.git(ctx, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff returns the unified diff from base to head.
func (m *Mirror) Diff(ctx context.Context, base, head string) ([]byte, error) {
	return m.git(ctx, "diff", "--no-color", "--no-ext-diff", base, head)
}

// ChangedFiles lists paths that differ between base and head, sorted.
func (m *Mirror) ChangedFiles(ctx context.Context, base, head string) ([]string, error) {
	out, err := m.git(ctx, "diff", "--name-only", "-z", "--no-renames", base, head)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Show returns a file's content at a commit. ok is false when the file
// doesn't exist there.
func (m *Mirror) Show(ctx context.Context, commit, path string) ([]byte, bool, error) {
	if _, err := m.git(ctx, "cat-file", "-e", commit+":"+path); err != nil {
		return nil, false, nil
	}
	out, err := m.git(ctx, "show", commit+":"+path)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// Worktree checks out commit (detached) into dir. The returned function
// removes the worktree.
func (m *Mirror) Worktree(ctx context.Context, commit, dir string) (func() error, error) {
	if _, err := m.git(ctx, "worktree", "add", "-q", "--detach", "--force", dir, commit); err != nil {
		return nil, err
	}
	return func() error {
		_, err := m.git(context.Background(), "worktree", "remove", "--force", dir)
		if err != nil {
			_ = os.RemoveAll(dir)
		}
		_, _ = m.git(context.Background(), "worktree", "prune")
		return err
	}, nil
}
