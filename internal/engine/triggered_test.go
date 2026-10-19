package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// triggeredFiles are the pipelines under .plumbix/pipelines/ the WU-045
// tests push.
var triggeredFiles = map[string]string{
	".plumbix/pipelines/deploy.yaml":   "version: 1\non: {push: {branches: [main, \"release/*\"], paths: [\"svc/**\"]}}\nsteps:\n  - {name: deploy-svc, image: x, run: x}\n",
	".plumbix/pipelines/web.yaml":      "version: 1\non: {push: {branches: [main], paths: [\"web/**\"]}}\nsteps:\n  - {name: web-build, image: x, run: x}\n",
	".plumbix/pipelines/off.yaml":      "version: 1\non: {push: {branches: [\"**\"]}, manual: true}\nsteps:\n  - {name: off-step, image: x, run: x, disabled: {reason: docker}}\n",
	".plumbix/pipelines/bad.yaml":      "version: 1\nsteps:\n  - {name: bad-step, image: x, run: x}\n",
	".plumbix/pipelines/handonly.yaml": "version: 1\non: {manual: true}\nsteps:\n  - {name: hand-step, image: x, run: x}\n",
	".plumbix/pipelines/README.md":     "not a pipeline\n",
}

func jobNames(h *harness) []string {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	var names []string
	for _, j := range h.fake.jobs {
		names = append(names, j.Name)
	}
	return names
}

// TestPushStartsMatchingTriggeredPipelines (WU-045).
func TestPushStartsMatchingTriggeredPipelines(t *testing.T) {
	h := newHarness(t, Config{})
	before := h.repo.Commit("main", triggeredFiles, "add pipelines")
	after := h.repo.Commit("main", map[string]string{"svc/a.go": "package a\n"}, "change svc")
	runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", Before: before, After: after}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Pipeline != "deploy" || runs[0].Branch != "main" {
		t.Fatalf("runs = %+v, want deploy only", runs)
	}
	got := h.wait(runs[0].ID)
	if got.Status != run.Finished || got.Head != after || got.Pipeline != "deploy" || len(got.Steps) != 1 || got.Steps[0].Name != "deploy-svc" {
		t.Fatalf("run = %+v", got)
	}
	if names := jobNames(h); !slices.Equal(names, []string{"deploy-svc"}) {
		t.Errorf("jobs = %v", names)
	}

	// A push that changes nothing a pipeline watches starts nothing.
	quiet := h.repo.Commit("main", map[string]string{"docs/a.md": "a\n"}, "docs")
	if runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", Before: after, After: quiet}}); err != nil || len(runs) != 0 {
		t.Errorf("docs push started %+v, %v", runs, err)
	}
}

func TestNewBranchIgnoresPaths(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("main", triggeredFiles, "add pipelines")
	head := h.repo.Commit("release/1.0", map[string]string{"docs/a.md": "a\n"}, "branch off")
	runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "release/1.0", After: head}})
	if err != nil || len(runs) != 1 || runs[0].Pipeline != "deploy" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	h.wait(runs[0].ID)
}

func TestManualRun(t *testing.T) {
	h := newHarness(t, Config{})
	head := h.repo.Commit("main", triggeredFiles, "add pipelines")
	r, err := h.eng.RunPipeline(ctx, h.repoID, "handonly", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(r.ID)
	if got.Status != run.Finished || got.Pipeline != "handonly" || got.Head != head || len(got.Steps) != 1 {
		t.Fatalf("run = %+v", got)
	}
	var ve *pipeline.ValidationError
	for name, want := range map[string]error{"nope": change.ErrNotFound, "deploy": ErrNotManual, "off": ErrPipelineDisabled, "bad": ve} {
		_, err := h.eng.RunPipeline(ctx, h.repoID, name, "main")
		switch {
		case name == "bad":
			if !errors.As(err, &ve) {
				t.Errorf("%s: err = %v, want a validation error", name, err)
			}
		case !errors.Is(err, want):
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}
	if _, err := h.eng.RunPipeline(ctx, h.repoID, "handonly", "missing"); err == nil {
		t.Error("an unknown branch started a run")
	}
}

func TestTriggeredPipelinesLists(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("main", triggeredFiles, "add pipelines")
	v, err := h.eng.TriggeredPipelines(ctx, h.repoID, "main")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range v.Pipelines {
		names = append(names, p.Name)
		switch p.Name {
		case "bad":
			if p.Pipeline != nil || len(p.Problems) == 0 {
				t.Errorf("bad = %+v", p)
			}
		case "off":
			if p.Pipeline == nil || !p.Pipeline.Disabled() || p.File != ".plumbix/pipelines/off.yaml" {
				t.Errorf("off = %+v", p)
			}
		}
	}
	if !slices.Equal(names, []string{"bad", "deploy", "handonly", "off", "web"}) || v.SHA == "" {
		t.Errorf("names = %v, sha %q", names, v.SHA)
	}
}

// TestOnePushOfSeveralBranchesFetchesOnceAndStartsEach (WU-045 review):
// moves are handled together; a move that fails does not stop the others.
func TestOnePushOfSeveralBranchesFetchesOnceAndStartsEach(t *testing.T) {
	h := newHarness(t, Config{})
	base := h.repo.Commit("main", triggeredFiles, "add pipelines")
	main := h.repo.Commit("main", map[string]string{"svc/a.go": "package a\n"}, "svc")
	rel := h.repo.Commit("release/2.0", map[string]string{"svc/b.go": "package b\n"}, "release")
	runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{
		{Branch: "gone", Before: base, After: "0000000000000000000000000000000000000bad"},
		{Branch: "main", Before: base, After: main},
		{Branch: "release/2.0", After: rel},
	})
	if err == nil {
		t.Error("the unknown commit's error was dropped")
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.Branch+":"+r.Pipeline)
		h.wait(r.ID)
	}
	if !slices.Equal(got, []string{"main:deploy", "release/2.0:deploy"}) {
		t.Errorf("runs = %v, want deploy on main and release/2.0 despite the failed move", got)
	}
}
