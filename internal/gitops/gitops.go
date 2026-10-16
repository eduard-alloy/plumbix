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
	dir     string
	url     string
	auth    *Auth
	askpass string
}

// Auth is a username and token for HTTPS remotes.
type Auth struct {
	User  string
	Token string
}

// AskpassFile is the askpass script's name inside the data directory.
const AskpassFile = "git-askpass.sh"

// askpassScript answers git's prompts from environment variables that are
// set only for the git process. It contains no secret itself.
const askpassScript = `#!/bin/sh
case "$1" in
  Username*) printf '%s\n' "$PLUMBIX_GIT_USER" ;;
  *) printf '%s\n' "$PLUMBIX_GIT_TOKEN" ;;
esac
`

// EnsureAskpass writes the askpass script into dir (mode 0700) and returns
// its path.
func EnsureAskpass(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, AskpassFile)
	if err := os.WriteFile(p, []byte(askpassScript), 0o700); err != nil {
		return "", fmt.Errorf("write askpass script: %w", err)
	}
	if err := os.Chmod(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// Open returns the mirror at dir for the remote url. Nothing is created
// until Fetch.
func Open(dir, url string) *Mirror { return &Mirror{dir: dir, url: url} }

// WithAuth makes git authenticate with a, answering prompts through the
// askpass script at askpass.
func (m *Mirror) WithAuth(a Auth, askpass string) *Mirror {
	m.auth, m.askpass = &a, askpass
	return m
}

// command builds a git command. Credentials go only into the process
// environment, never into its arguments, and personal credential helpers
// are switched off.
func (m *Mirror) command(ctx context.Context, args ...string) *exec.Cmd {
	full := []string{"-c", "credential.helper="}
	if _, err := os.Stat(m.dir); err == nil {
		full = append(full, "-C", m.dir)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		// Never let SSH wait for a password or a host key prompt.
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	if m.auth != nil {
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+m.askpass, "PLUMBIX_GIT_USER="+m.auth.User, "PLUMBIX_GIT_TOKEN="+m.auth.Token)
	}
	return cmd
}

// git runs a git command in the mirror and returns stdout.
func (m *Mirror) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := m.command(ctx, args...)
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

// ListHeads asks the remote for its branch heads without fetching objects.
func (m *Mirror) ListHeads(ctx context.Context) (map[string]string, error) {
	out, err := m.git(ctx, "ls-remote", "--heads", m.url)
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			heads[branch] = sha
		}
	}
	return heads, nil
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
