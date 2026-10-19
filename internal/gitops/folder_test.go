package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestInspectFolder (WU-041): a working tree reports its origin, its
// default branch and whether the pipeline file is in HEAD.
func TestInspectFolder(t *testing.T) {
	r := gittest.New(t)
	r.Commit("main", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps: []\n"}, "pipeline")
	dir := filepath.Join(t.TempDir(), "app")
	gitIn(t, filepath.Dir(dir), "clone", "-q", r.Remote, dir)
	f, err := InspectFolder(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Remote != r.Remote || f.DefaultBranch != "main" || !f.HasPipeline {
		t.Errorf("folder = %+v", f)
	}
	// A folder whose HEAD has no pipeline file, on another branch, with no origin but another remote.
	bare := filepath.Join(t.TempDir(), "plain")
	gitIn(t, filepath.Dir(bare), "init", "-q", "--initial-branch=trunk", bare)
	if err := os.WriteFile(filepath.Join(bare, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, bare, "add", ".")
	gitIn(t, bare, "commit", "-q", "-m", "a")
	if f, err := InspectFolder(ctx, bare); err != nil || f.Remote != "" || f.DefaultBranch != "trunk" || f.HasPipeline {
		t.Errorf("folder without a remote = %+v %v", f, err)
	}
	gitIn(t, bare, "remote", "add", "upstream", "git@git.example.com:team/app.git")
	if f, _ := InspectFolder(ctx, bare); f.Remote != "git@git.example.com:team/app.git" {
		t.Errorf("first remote = %q", f.Remote)
	}
	if _, err := InspectFolder(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a git working tree") {
		t.Errorf("plain folder: %v", err)
	}
	// A folder inside a working tree is not the repository: its top folder is.
	sub := filepath.Join(dir, ".plumbix")
	if _, err := InspectFolder(ctx, sub); err == nil || !strings.Contains(err.Error(), "inside the repository at") {
		t.Errorf("subfolder: %v", err)
	}
}

// TestCloneIntoANewOrEmptyFolderOnly: a clone lands in a new folder, with a
// token through askpass, never into a folder that has files.
func TestCloneIntoANewOrEmptyFolderOnly(t *testing.T) {
	r := gittest.New(t)
	url := r.ServeHTTP("x-access-token", secretToken)
	ap := askpass(t)
	dest := filepath.Join(t.TempDir(), "clones", "app")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Clone(ctx, url, dest, &Auth{User: "x-access-token", Token: secretToken}, ap); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("clone has no README: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if strings.Contains(string(cfg), secretToken) {
		t.Error("the token was written into the clone's config")
	}
	empty := t.TempDir()
	if err := Clone(ctx, r.Remote, empty, nil, ""); err != nil {
		t.Errorf("clone into an empty folder: %v", err)
	}
	if err := Clone(ctx, r.Remote, dest, nil, ""); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("clone into a folder with files: %v", err)
	}
	err := Clone(ctx, url, filepath.Join(t.TempDir(), "x"), &Auth{User: "x-access-token", Token: "wrong"}, ap)
	if err == nil || strings.Contains(err.Error(), secretToken) || strings.Contains(err.Error(), "wrong") {
		t.Errorf("wrong token: %v", err)
	}
}

// TestPublishAddsOriginAndPushesEveryBranch (WU-042): a folder without a
// remote gains origin, every branch is pushed, the checked-out one tracks
// it; a folder that already has origin is refused.
func TestPublishAddsOriginAndPushesEveryBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	gitIn(t, filepath.Dir(dir), "init", "-q", "--initial-branch=main", dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "a")
	gitIn(t, dir, "branch", "draft")
	remote := filepath.Join(t.TempDir(), "notes.git")
	gitIn(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	pushed, err := Publish(ctx, dir, remote, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pushed, ",") != "draft,main" {
		t.Errorf("pushed = %v", pushed)
	}
	f, _ := InspectFolder(ctx, dir)
	if f.Remote != remote {
		t.Errorf("origin = %q", f.Remote)
	}
	out, _ := exec.Command("git", "-C", remote, "for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if strings.Join(strings.Fields(string(out)), ",") != "draft,main" {
		t.Errorf("remote branches = %q", out)
	}
	up, _ := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "main@{upstream}").Output()
	if strings.TrimSpace(string(up)) != "origin/main" {
		t.Errorf("upstream = %q", up)
	}
	if _, err := Publish(ctx, dir, remote, nil, ""); err == nil || !strings.Contains(err.Error(), "already has a remote") {
		t.Errorf("second publish: %v", err)
	}
	if b, err := LocalBranches(ctx, dir); err != nil || strings.Join(b, ",") != "draft,main" {
		t.Errorf("local branches = %v %v", b, err)
	}
}

// TestPublishWithAnUnbornHead: checked out on a branch with no commit yet,
// the other branches are pushed and nothing fails over the missing upstream.
func TestPublishWithAnUnbornHead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	gitIn(t, filepath.Dir(dir), "init", "-q", "--initial-branch=main", dir)
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "a")
	gitIn(t, dir, "checkout", "-q", "--orphan", "fresh")
	remote := filepath.Join(t.TempDir(), "notes.git")
	gitIn(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	pushed, err := Publish(ctx, dir, remote, nil, "")
	if err != nil || strings.Join(pushed, ",") != "main" {
		t.Errorf("publish = %v %v", pushed, err)
	}
}
