package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

var ctx = context.Background()

func TestFetchHeadAndMergeBase(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"a.txt": "a\n"}, "main work")
	head := r.Commit("feat", map[string]string{"b.txt": "b\n"}, "feature work")
	mainNow := r.Commit("main", map[string]string{"c.txt": "c\n"}, "more main work")

	m := Open(filepath.Join(t.TempDir(), "mirror.git"), r.Remote)
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Head(ctx, "feat"); err != nil || got != head {
		t.Errorf("feat head = %s, %v; want %s", got, err, head)
	}
	if got, _ := m.Head(ctx, "main"); got != mainNow {
		t.Errorf("main head = %s, want %s", got, mainNow)
	}
	if got, err := m.MergeBase(ctx, mainNow, head); err != nil || got != base {
		t.Errorf("merge base = %s, %v; want %s", got, err, base)
	}
	if _, err := m.Head(ctx, "nope"); !errors.Is(err, ErrUnknownBranch) {
		t.Errorf("unknown branch err = %v", err)
	}

	// A second fetch picks up new commits and removes deleted branches.
	newer := r.Commit("feat", map[string]string{"b.txt": "b2\n"}, "update")
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Head(ctx, "feat"); got != newer {
		t.Errorf("after refetch feat = %s, want %s", got, newer)
	}
}

func TestDiffChangedFilesAndShow(t *testing.T) {
	r := gittest.New(t)
	base := r.Commit("main", map[string]string{"keep.txt": "keep\n", "drop.txt": "drop\n"}, "base")
	head := r.Commit("feat", map[string]string{"new.go": "package x\n", "drop.txt": "", "keep.txt": "changed\n"}, "change")
	m := Open(filepath.Join(t.TempDir(), "mirror.git"), r.Remote)
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := m.ChangedFiles(ctx, base, head)
	if err != nil || !reflect.DeepEqual(files, []string{"drop.txt", "keep.txt", "new.go"}) {
		t.Errorf("changed files = %v, %v", files, err)
	}
	diff, err := m.Diff(ctx, base, head)
	if err != nil || !strings.Contains(string(diff), "+changed") || !strings.Contains(string(diff), "new.go") {
		t.Errorf("diff = %s, %v", diff, err)
	}
	content, ok, err := m.Show(ctx, head, "keep.txt")
	if err != nil || !ok || string(content) != "changed\n" {
		t.Errorf("show keep.txt = %q %v %v", content, ok, err)
	}
	if _, ok, err := m.Show(ctx, head, "drop.txt"); ok || err != nil {
		t.Errorf("show deleted file: ok=%v err=%v", ok, err)
	}
}

func TestWorktree(t *testing.T) {
	r := gittest.New(t)
	head := r.Commit("feat", map[string]string{"src/app.go": "package app\n"}, "app")
	m := Open(filepath.Join(t.TempDir(), "mirror.git"), r.Remote)
	if err := m.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ws")
	remove, err := m.Worktree(ctx, head, dir)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "src", "app.go")); err != nil || string(data) != "package app\n" {
		t.Fatalf("worktree file = %q, %v", data, err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree still exists: %v", err)
	}
}

func TestFetchBadURL(t *testing.T) {
	m := Open(filepath.Join(t.TempDir(), "mirror.git"), filepath.Join(t.TempDir(), "does-not-exist.git"))
	if err := m.Fetch(ctx); err == nil {
		t.Fatal("expected fetch error")
	}
}

// TestGitRunsOutsideTheCallersRepository: before the mirror exists, git
// must not care where the process runs. Plumbix's own pipeline runs the
// tests inside a worktree whose git directory is on the host, where every
// git command run in that directory fails.
func TestGitRunsOutsideTheCallersRepository(t *testing.T) {
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nonexistent/mirror.git/worktrees/workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(broken)
	r := gittest.New(t)
	want := r.Commit("feat", map[string]string{"a.txt": "a\n"}, "a")
	m := Open(filepath.Join(t.TempDir(), "never-created.git"), r.Remote)
	heads, err := m.ListHeads(ctx)
	if err != nil {
		t.Fatalf("ListHeads from inside a broken worktree: %v", err)
	}
	if heads["feat"] != want {
		t.Errorf("heads = %v", heads)
	}
	if err := m.Fetch(ctx); err != nil {
		t.Fatalf("Fetch from inside a broken worktree: %v", err)
	}
}

// TestInheritedGitVariablesAreDropped: GIT_DIR and friends in the server's
// environment must not point git away from the mirror.
func TestInheritedGitVariablesAreDropped(t *testing.T) {
	r := gittest.New(t)
	want := r.Commit("feat", map[string]string{"a.txt": "a\n"}, "a")
	// Set after the test repository exists: the helper's own git would be
	// misled too, which is the point.
	t.Setenv("GIT_DIR", "/nonexistent/elsewhere.git")
	t.Setenv("GIT_WORK_TREE", "/nonexistent/tree")
	m := Open(filepath.Join(t.TempDir(), "mirror.git"), r.Remote)
	if err := m.Fetch(ctx); err != nil {
		t.Fatalf("Fetch with GIT_DIR set: %v", err)
	}
	if head, err := m.Head(ctx, "feat"); err != nil || head != want {
		t.Fatalf("Head = %s, %v; want %s", head, err, want)
	}
}
