package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
)

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
		if strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), "stale info") {
			return fmt.Errorf("%w: %s", ErrPushRejected, branch)
		}
		return err
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
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
