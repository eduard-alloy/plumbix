package gitops

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

func mirrorOf(t *testing.T, remote string) *Mirror {
	t.Helper()
	m := Open(filepath.Join(t.TempDir(), "mirror.git"), remote)
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	return m
}

func treeOf(t *testing.T, m *Mirror, commit string) string {
	t.Helper()
	out, err := m.git(ctx, "rev-parse", commit+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func remoteHead(t *testing.T, m *Mirror, branch string) string {
	t.Helper()
	heads, err := m.ListHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return heads[branch]
}

const message = "Add feature\n\nBecause we need it.\n\nPlumbix-Change: c_abc\nPlumbix-Run: run_abc\n"

func TestSquashAndPush(t *testing.T) {
	r := gittest.New(t)
	oldMain := r.Commit("main", map[string]string{"a.txt": "a\n"}, "main work")
	head := r.Commit("feat", map[string]string{"b.txt": "b\n"}, "feature work")
	m := mirrorOf(t, r.Remote)

	sha, err := m.Squash(ctx, head, oldMain, message)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Commit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parents) != 1 || c.Parents[0] != oldMain {
		t.Errorf("parents = %v, want [%s]", c.Parents, oldMain)
	}
	if treeOf(t, m, sha) != treeOf(t, m, head) {
		t.Error("squash commit does not carry the validated tree")
	}
	if c.Author != "Test" || c.Subject != "Add feature" || !strings.Contains(c.Message, "Plumbix-Change: c_abc") {
		t.Errorf("commit = %+v", c)
	}
	if committer, _ := m.git(ctx, "log", "-1", "--format=%cn <%ce>", sha); strings.TrimSpace(string(committer)) != "Plumbix <plumbix@localhost>" {
		t.Errorf("committer = %q", committer)
	}

	if err := m.Push(ctx, "main", sha, oldMain); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead(t, m, "main"); got != sha {
		t.Errorf("remote main = %s, want %s", got, sha)
	}
	if got, _ := m.Head(ctx, "main"); got != sha {
		t.Errorf("mirror main = %s, want %s", got, sha)
	}
}

func TestMergeCommitAndFastForward(t *testing.T) {
	r := gittest.New(t)
	oldMain := r.Commit("main", map[string]string{"a.txt": "a\n"}, "main work")
	head := r.Commit("feat", map[string]string{"b.txt": "b\n"}, "feature work")
	m := mirrorOf(t, r.Remote)

	sha, err := m.MergeCommit(ctx, head, oldMain, message)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Commit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parents) != 2 || c.Parents[0] != oldMain || c.Parents[1] != head {
		t.Errorf("parents = %v, want [%s %s]", c.Parents, oldMain, head)
	}
	if treeOf(t, m, sha) != treeOf(t, m, head) {
		t.Error("merge commit does not carry the validated tree")
	}

	// A fast-forward is a push of the head itself.
	if err := m.Push(ctx, "main", head, oldMain); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead(t, m, "main"); got != head {
		t.Errorf("remote main = %s, want %s", got, head)
	}
}

func TestMergeRefusesWhenTargetNotContained(t *testing.T) {
	r := gittest.New(t)
	r.Commit("feat", map[string]string{"b.txt": "b\n"}, "feature work")
	newMain := r.Commit("main", map[string]string{"a.txt": "a\n"}, "main moved on")
	m := mirrorOf(t, r.Remote)
	head, _ := m.Head(ctx, "feat")
	if _, err := m.Squash(ctx, head, newMain, message); !errors.Is(err, ErrNotContained) {
		t.Errorf("squash err = %v", err)
	}
	if _, err := m.MergeCommit(ctx, head, newMain, message); !errors.Is(err, ErrNotContained) {
		t.Errorf("merge err = %v", err)
	}
}

func TestPushRefusesStaleLease(t *testing.T) {
	r := gittest.New(t)
	oldMain := r.Commit("main", map[string]string{"a.txt": "a\n"}, "main work")
	head := r.Commit("feat", map[string]string{"b.txt": "b\n"}, "feature work")
	m := mirrorOf(t, r.Remote)
	moved := r.Commit("main", map[string]string{"c.txt": "c\n"}, "someone pushed")

	err := m.Push(ctx, "main", head, oldMain)
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("err = %v", err)
	}
	if got := remoteHead(t, m, "main"); got != moved {
		t.Errorf("remote main = %s, want %s untouched", got, moved)
	}
	if got, _ := m.Head(ctx, "main"); got != oldMain {
		t.Errorf("mirror main = %s, want %s untouched", got, oldMain)
	}
}

func TestCommits(t *testing.T) {
	r := gittest.New(t)
	c1 := r.Commit("main", map[string]string{"a.txt": "a\n"}, "first")
	c2 := r.Commit("main", map[string]string{"b.txt": "b\n"}, "second")
	c3 := r.Commit("main", map[string]string{"c.txt": "c\n"}, "third")
	m := mirrorOf(t, r.Remote)

	got, err := m.Commits(ctx, c1, c3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SHA != c3 || got[1].SHA != c2 {
		t.Fatalf("commits = %+v", got)
	}
	if got[0].Author != "Test" || got[0].Email != "test@example.com" || got[0].Subject != "third" || got[0].Time.IsZero() {
		t.Errorf("commit = %+v", got[0])
	}
	tip, err := m.Commits(ctx, "", c3)
	if err != nil || len(tip) != 1 || tip[0].SHA != c3 {
		t.Errorf("tip only = %+v, %v", tip, err)
	}
}

func TestPushErrorsAreClassified(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", ErrPushDenied},
		{"remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/o/r/'", ErrPushDenied},
		{"remote: Permission to o/r.git denied to someone.\nfatal: unable to access 'https://github.com/o/r/': The requested URL returned error: 403", ErrPushDenied},
		{"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.", ErrPushDenied},
		{"! [rejected] main -> main (stale info)", ErrPushRejected},
	}
	for _, c := range cases {
		err := pushError(fmt.Errorf("git push: exit status 128: %s", c.stderr), "main")
		if !errors.Is(err, c.want) {
			t.Errorf("%q: err = %v, want %v", c.stderr, err, c.want)
		}
		if errors.Is(err, ErrPushDenied) && strings.Contains(err.Error(), "github.com") {
			t.Errorf("%q: the denied error repeats git's output: %v", c.stderr, err)
		}
	}
	other := errors.New("git push: exit status 1: something else")
	if err := pushError(other, "main"); err != other {
		t.Errorf("unrelated error was rewritten: %v", err)
	}
}
