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

func TestCommitFileReplacesOneBlobOnTheParent(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"a.txt": "a\n", "dir/keep.txt": "keep\n", ".plumbix/pipeline.yaml": "version: 1\n"}, "base")
	m := mirrorOf(t, r.Remote)
	sha, err := m.CommitFile(ctx, base, ".plumbix/pipeline.yaml", []byte("version: 1\nsteps: []\n"), "Pipeline: add a step")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Show(ctx, sha, ".plumbix/pipeline.yaml")
	if err != nil || !ok || string(got) != "version: 1\nsteps: []\n" {
		t.Fatalf("file at new commit = %q %v %v", got, ok, err)
	}
	for _, p := range []string{"a.txt", "dir/keep.txt"} {
		if _, ok, _ := m.Show(ctx, sha, p); !ok {
			t.Errorf("%s missing from the new commit", p)
		}
	}
	files, err := m.ChangedFiles(ctx, base, sha)
	if err != nil || len(files) != 1 || files[0] != ".plumbix/pipeline.yaml" {
		t.Errorf("changed files = %v %v, want only the pipeline", files, err)
	}
	c, err := m.Commit(ctx, sha)
	if err != nil || c.Subject != "Pipeline: add a step" || c.Author != CommitterName || len(c.Parents) != 1 || c.Parents[0] != base {
		t.Errorf("commit = %+v %v", c, err)
	}
	if h, _ := m.Head(ctx, "main"); h != base {
		t.Errorf("main moved to %s", h)
	}
	// A path that did not exist is added.
	added, err := m.CommitFile(ctx, base, "new/file.txt", []byte("x"), "add")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := m.Show(ctx, added, "new/file.txt"); !ok || string(got) != "x" {
		t.Errorf("new file = %q %v", got, ok)
	}
	if _, err := m.CommitFile(ctx, base, "../escape", []byte("x"), "bad"); err == nil {
		t.Error("a path outside the tree was accepted")
	}
}

func TestPushBranchCreatesAndNeverOverwrites(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"a.txt": "a\n"}, "base")
	m := mirrorOf(t, r.Remote)
	sha, err := m.CommitFile(ctx, base, "f.txt", []byte("f"), "f")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PushBranch(ctx, "pipeline/new", sha); err != nil {
		t.Fatal(err)
	}
	if got := remoteHead(t, m, "pipeline/new"); got != sha {
		t.Errorf("remote branch = %s, want %s", got, sha)
	}
	if got, _ := m.Head(ctx, "pipeline/new"); got != sha {
		t.Errorf("mirror branch = %s, want %s", got, sha)
	}
	other, _ := m.CommitFile(ctx, base, "g.txt", []byte("g"), "g")
	if err := m.PushBranch(ctx, "pipeline/new", other); !errors.Is(err, ErrBranchExists) {
		t.Errorf("push onto an existing branch: err = %v, want ErrBranchExists", err)
	}
	if got := remoteHead(t, m, "pipeline/new"); got != sha {
		t.Errorf("existing branch was changed to %s", got)
	}
	if err := m.PushBranch(ctx, "main", other); !errors.Is(err, ErrBranchExists) {
		t.Errorf("push onto main: err = %v", err)
	}
}

func TestPushBranchRefusesABranchThatAppearedAfterTheFetch(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"a.txt": "a\n"}, "base")
	m := mirrorOf(t, r.Remote)
	sha, err := m.CommitFile(ctx, base, "f.txt", []byte("f"), "f")
	if err != nil {
		t.Fatal(err)
	}
	// Someone else creates the branch on the remote after the mirror's fetch.
	theirs := r.Commit("pipeline/raced", map[string]string{"g.txt": "g\n"}, "theirs")
	if err := m.PushBranch(ctx, "pipeline/raced", sha); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("err = %v, want ErrBranchExists", err)
	}
	if got := remoteHead(t, m, "pipeline/raced"); got != theirs {
		t.Errorf("remote branch = %s, want %s untouched", got, theirs)
	}
	if _, err := m.Head(ctx, "pipeline/raced"); err == nil {
		t.Error("the mirror learned a branch the push did not create")
	}
}

func TestMergeBranchesMergesAndReportsConflicts(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"a.txt": "a\n", "shared.txt": "one\n"}, "base")
	m := mirrorOf(t, r.Remote)
	ours, err := m.CommitFile(ctx, base, "f.txt", []byte("f\n"), "ours")
	if err != nil {
		t.Fatal(err)
	}
	theirs := r.Commit("main", map[string]string{"b.txt": "b\n"}, "main goes on")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := m.MergeBranches(ctx, ours, theirs, "Merge main into pipeline/x")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Commit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parents) != 2 || c.Parents[0] != ours || c.Parents[1] != theirs || c.Author != CommitterName || c.Email != CommitterEmail || c.Subject != "Merge main into pipeline/x" {
		t.Errorf("merge commit = %+v", c)
	}
	for _, f := range []string{"a.txt", "b.txt", "f.txt", "shared.txt"} {
		if _, ok, err := m.Show(ctx, sha, f); err != nil || !ok {
			t.Errorf("%s missing from the merged tree (%v)", f, err)
		}
	}
	// Both sides edit the same line: no merge, and the path is named.
	mine, _ := m.CommitFile(ctx, base, "shared.txt", []byte("mine\n"), "mine")
	other := r.Commit("main", map[string]string{"shared.txt": "other\n"}, "other")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = m.MergeBranches(ctx, mine, other, "m")
	if !errors.Is(err, ErrMergeConflict) || !strings.Contains(err.Error(), "shared.txt") {
		t.Errorf("conflict: err = %v, want ErrMergeConflict naming shared.txt", err)
	}
	// Already contained: a merge of an ancestor is refused rather than an empty merge commit.
	if _, err := m.MergeBranches(ctx, ours, base, "m"); !errors.Is(err, ErrNotContained) {
		t.Errorf("merging an ancestor: err = %v, want ErrNotContained", err)
	}
}
