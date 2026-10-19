package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Errors from merging and pushing.
var (
	// ErrNotContained means the target commit is not an ancestor of the
	// head, so landing the head's tree would drop the target's changes.
	ErrNotContained = errors.New("target is not contained in the validated commit")
	// ErrPushRejected means the remote branch no longer points at the
	// expected commit. Nothing was changed.
	ErrPushRejected = errors.New("push rejected: the remote branch moved")
	// ErrPushDenied means the host refused Plumbix's credentials, or there
	// were none to offer. Nothing was changed.
	ErrPushDenied = errors.New("push denied: the host did not accept Plumbix's credentials")
	// ErrBranchExists means a branch Plumbix wanted to create is already on
	// the remote. Nothing was changed.
	ErrBranchExists = errors.New("branch exists")
	// ErrMergeConflict means two branches change the same lines, so no merge
	// commit was made. The error names the paths.
	ErrMergeConflict = errors.New("merge conflict")
)

// deniedMarkers are the phrases git prints when a remote refuses the
// credentials: a bad token, a token without push rights, or an SSH key the
// host does not know. No token at all arrives as ErrNoCredentials.
var deniedMarkers = []string{
	"Authentication failed",
	"denied to",
	"returned error: 403",
	"Permission denied (publickey)",
}

// pushError turns git's push failure into one of the push errors. The denied
// error does not repeat git's output, which names the remote.
func pushError(err error, branch string) error {
	// No token to offer: the runner already turned git's prompt into ErrNoCredentials.
	if errors.Is(err, ErrNoCredentials) {
		return fmt.Errorf("%w: %s", ErrPushDenied, branch)
	}
	msg := err.Error()
	for _, m := range deniedMarkers {
		if strings.Contains(msg, m) {
			return fmt.Errorf("%w: %s", ErrPushDenied, branch)
		}
	}
	if strings.Contains(msg, "rejected") || strings.Contains(msg, "stale info") {
		return fmt.Errorf("%w: %s", ErrPushRejected, branch)
	}
	return err
}

// Committer identity for the commits Plumbix creates.
const (
	CommitterName  = "Plumbix"
	CommitterEmail = "plumbix@localhost"
)

// Commit describes one commit.
type Commit struct {
	SHA     string    `json:"sha"`
	Parents []string  `json:"parents,omitempty"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject"`
	Message string    `json:"-"`
}

// commitFormat lists the fields Commit and Commits read, separated by
// the unit separator so subjects can hold anything but a newline.
const commitFormat = "%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%B%x1e"

// Commit returns one commit.
func (m *Mirror) Commit(ctx context.Context, sha string) (Commit, error) {
	cs, err := m.Commits(ctx, "", sha)
	if err != nil {
		return Commit{}, err
	}
	if len(cs) != 1 {
		return Commit{}, fmt.Errorf("commit %s not found", short(sha))
	}
	return cs[0], nil
}

// Commits lists the first-parent commits reachable from to but not from
// from, newest first. An empty from lists only to itself.
func (m *Mirror) Commits(ctx context.Context, from, to string) ([]Commit, error) {
	args := []string{"log", "--first-parent", "--format=" + commitFormat}
	if from == "" {
		args = append(args, "-1", to)
	} else {
		args = append(args, from+".."+to)
	}
	out, err := m.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) != 7 {
			continue
		}
		c := Commit{SHA: f[0], Author: f[2], Email: f[3], Subject: f[5], Message: f[6]}
		if f[1] != "" {
			c.Parents = strings.Fields(f[1])
		}
		if c.Time, err = time.Parse(time.RFC3339, f[4]); err != nil {
			return nil, fmt.Errorf("commit %s: author date: %w", short(c.SHA), err)
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Squash creates one commit carrying head's tree on top of onto, authored
// by head's author and committed by Plumbix. onto must be an ancestor of
// head, so that the tree landing is exactly the validated one.
func (m *Mirror) Squash(ctx context.Context, head, onto, message string) (string, error) {
	return m.commitTree(ctx, head, onto, message, []string{onto})
}

// MergeCommit creates a merge commit of head into onto that carries head's
// tree. onto must be an ancestor of head.
func (m *Mirror) MergeCommit(ctx context.Context, head, onto, message string) (string, error) {
	return m.commitTree(ctx, head, onto, message, []string{onto, head})
}

func (m *Mirror) commitTree(ctx context.Context, head, onto, message string, parents []string) (string, error) {
	if _, err := m.git(ctx, "merge-base", "--is-ancestor", onto, head); err != nil {
		return "", fmt.Errorf("%w: %s is not an ancestor of %s", ErrNotContained, short(onto), short(head))
	}
	author, err := m.Commit(ctx, head)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", head + "^{tree}", "-F", "-"}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Author, "GIT_AUTHOR_EMAIL=" + author.Email, "GIT_AUTHOR_DATE=" + author.Time.Format(time.RFC3339),
		"GIT_COMMITTER_NAME=" + CommitterName, "GIT_COMMITTER_EMAIL=" + CommitterEmail,
	}
	out, err := m.run(ctx, strings.NewReader(message), env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Push moves the remote branch from old to sha, refusing if the remote no
// longer points at old. On success the mirror's branch follows.
func (m *Mirror) Push(ctx context.Context, branch, sha, old string) error {
	ref := "refs/heads/" + branch
	_, err := m.git(ctx, "push", "-q", m.url, sha+":"+ref, "--force-with-lease="+ref+":"+old)
	if err != nil {
		return pushError(err, branch)
	}
	_, err = m.git(ctx, "update-ref", ref, sha, old)
	return err
}

// run executes git with stdin and extra environment, returning stdout.
func (m *Mirror) run(ctx context.Context, stdin io.Reader, env []string, args ...string) ([]byte, error) {
	cmd := m.command(ctx, args...)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		// Without credentials git asks for a username, which Plumbix forbids;
		// say what is missing instead of git's prompt message (WU-043).
		if strings.Contains(msg, "could not read Username") || strings.Contains(msg, "terminal prompts disabled") {
			return nil, fmt.Errorf("git %s: %w", args[0], ErrNoCredentials)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(msg))
	}
	return stdout.Bytes(), nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// CommitFile returns a new commit on parent whose tree is the parent's with
// one file replaced or added. Plumbix is author and committer. Nothing moves:
// the caller decides which branch, if any, points at the commit.
func (m *Mirror) CommitFile(ctx context.Context, parent, path string, content []byte, message string) (string, error) {
	if path == "" || strings.HasPrefix(path, "/") || hasDotDotSegment(path) {
		return "", fmt.Errorf("invalid path %q", path)
	}
	index, err := os.CreateTemp("", "plumbix-index-")
	if err != nil {
		return "", err
	}
	index.Close()
	defer os.Remove(index.Name())
	env := []string{"GIT_INDEX_FILE=" + index.Name()}
	if _, err := m.run(ctx, nil, env, "read-tree", parent+"^{tree}"); err != nil {
		return "", err
	}
	blob, err := m.run(ctx, bytes.NewReader(content), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err := m.run(ctx, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+","+path); err != nil {
		return "", err
	}
	tree, err := m.run(ctx, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	ident := []string{
		"GIT_AUTHOR_NAME=" + CommitterName, "GIT_AUTHOR_EMAIL=" + CommitterEmail,
		"GIT_COMMITTER_NAME=" + CommitterName, "GIT_COMMITTER_EMAIL=" + CommitterEmail,
	}
	out, err := m.run(ctx, strings.NewReader(message), ident, "commit-tree", strings.TrimSpace(string(tree)), "-p", parent, "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBranches creates a merge commit of theirs into ours, with Plumbix as
// author and committer, and returns it. Nothing moves: the caller decides
// which branch points at the commit. ErrNotContained when theirs is already
// contained in ours (a merge would add nothing); ErrMergeConflict, naming
// the paths, when the trees do not merge cleanly.
func (m *Mirror) MergeBranches(ctx context.Context, ours, theirs, message string) (string, error) {
	if _, err := m.git(ctx, "merge-base", "--is-ancestor", theirs, ours); err == nil {
		return "", fmt.Errorf("%w: %s already contains %s", ErrNotContained, short(ours), short(theirs))
	}
	// merge-tree writes the merged tree without a worktree. Exit 1 means
	// conflicts; the output then lists the tree, then the conflicted paths.
	cmd := m.command(ctx, "merge-tree", "--write-tree", "--name-only", "-z", ours, theirs)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return "", fmt.Errorf("git merge-tree: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("%w: %s", ErrMergeConflict, strings.Join(conflictedPaths(stdout.Bytes()), ", "))
	}
	out := stdout.Bytes()
	tree := strings.TrimSpace(strings.SplitN(string(out), "\x00", 2)[0])
	ident := []string{
		"GIT_AUTHOR_NAME=" + CommitterName, "GIT_AUTHOR_EMAIL=" + CommitterEmail,
		"GIT_COMMITTER_NAME=" + CommitterName, "GIT_COMMITTER_EMAIL=" + CommitterEmail,
	}
	sha, err := m.run(ctx, strings.NewReader(message), ident, "commit-tree", tree, "-p", ours, "-p", theirs, "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(sha)), nil
}

// conflictedPaths reads the paths from merge-tree's NUL-separated output:
// the tree id, then one path per conflicted file, then informational
// messages after an empty field.
func conflictedPaths(out []byte) []string {
	fields := strings.Split(string(out), "\x00")
	var paths []string
	for _, f := range fields[1:] {
		if f == "" {
			break
		}
		paths = append(paths, f)
	}
	if len(paths) == 0 {
		return []string{"unknown paths"}
	}
	return paths
}

// PushBranch creates branch on the remote at sha. It never forces: a branch
// the remote already has, by the mirror's last fetch or by the push itself,
// is ErrBranchExists. On success the mirror's branch follows.
func (m *Mirror) PushBranch(ctx context.Context, branch, sha string) error {
	if _, err := m.Head(ctx, branch); err == nil {
		return fmt.Errorf("%w: %s", ErrBranchExists, branch)
	}
	ref := "refs/heads/" + branch
	// An empty expected value makes the lease "the ref must not exist".
	if _, err := m.git(ctx, "push", "-q", m.url, sha+":"+ref, "--force-with-lease="+ref+":"); err != nil {
		perr := pushError(err, branch)
		if errors.Is(perr, ErrPushRejected) {
			return fmt.Errorf("%w: %s", ErrBranchExists, branch)
		}
		return perr
	}
	_, err := m.git(ctx, "update-ref", ref, sha)
	return err
}

func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
