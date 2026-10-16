//go:build integration

package engine

import (
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/store"
)

const dockerPipeline = `version: 1
steps:
  - {name: test, image: "alpine:3.20", run: "grep -q works app.txt"}
  - {name: validate, image: "plumbix-reference-validator:test", validator: true, needs: [test]}
`

// TestRealDockerLoop runs the whole loop with Docker and the real reference
// validator image: a good branch is approved, a broken one gets fixes.
func TestRealDockerLoop(t *testing.T) {
	build := exec.Command("docker", "build", "-q", "-f", "validators/reference/Dockerfile", "-t", "plumbix-reference-validator:test", ".")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	data := t.TempDir()
	db, err := store.Open(filepath.Join(data, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	docker, err := runner.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	eng := New(db, docker, Config{DataDir: data, MaxParallel: 2, DefaultTimeout: 2 * time.Minute, CPUs: 1, MemoryMB: 256, MaxFixRounds: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer eng.Shutdown(ctx)

	repo := gittest.New(t)
	repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": dockerPipeline}, "pipeline")
	repo.Commit("good", map[string]string{"app.txt": "it works\n"}, "good")
	repo.Commit("bad", map[string]string{"app.txt": "broken\n"}, "bad")
	repoID := "r_dockertest01"
	if err := db.CreateRepo(ctx, change.Repo{ID: repoID, Host: "github", URL: repo.Remote, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, db: db, svc: change.NewService(db), eng: eng, repoID: repoID}
	criteria := []change.Criterion{
		{Text: "the test step passes", Check: &change.Check{Steps: []string{"test"}}},
		{Text: "app.txt says it works", Check: &change.Check{Command: "grep -q works app.txt"}},
	}
	submit := func(branch string) change.Change {
		c, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: repoID, SourceBranch: branch, TargetBranch: "main", Title: branch, Intent: "i", AcceptanceCriteria: criteria})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	good := h.validate(submit("good").ID)
	if good.Outcome != change.Approved {
		t.Fatalf("good branch: %+v result %+v", good, good.Result)
	}
	merged, err := eng.Merge(ctx, good.ChangeID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if out, _ := exec.Command("git", "-C", repo.Remote, "rev-parse", "main").Output(); strings.TrimSpace(string(out)) != merged.Merge.Commit {
		t.Errorf("remote main = %s, merge commit %s", out, merged.Merge.Commit)
	}
	bad := h.validate(submit("bad").ID)
	if bad.Outcome != change.FixRequested || bad.Status != run.Finished {
		t.Fatalf("bad branch: %+v result %+v", bad, bad.Result)
	}
	for _, s := range bad.Steps {
		if s.Name == "validate" && s.Status != "succeeded" {
			t.Errorf("validator step %s: it must run and judge the failed test", s.Status)
		}
	}
	unmet := 0
	for _, c := range bad.Result.Criteria {
		if c.Status == "unmet" {
			unmet++
		}
	}
	if unmet != 2 {
		t.Errorf("bad branch criteria = %+v", bad.Result.Criteria)
	}
}
