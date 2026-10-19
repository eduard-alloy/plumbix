package engine

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/run"
)

const secretPipeline = "version: 1\non: {push: {branches: [\"**\"]}}\nsteps:\n  - {name: uses-token, image: x, run: x, secrets: [TOKEN]}\n"

func jobEnv(h *harness, name string) (map[string]string, bool) {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	for _, j := range h.fake.jobs {
		if j.Name == name {
			return j.Env, true
		}
	}
	return nil, false
}

// TestSecretsOnlyOnProtectedBranches (A38): a run on main gets the secret;
// a run on any other branch errors the step that names it, for triggered,
// plain branch and change runs alike.
func TestSecretsOnlyOnProtectedBranches(t *testing.T) {
	t.Run("triggered on main", func(t *testing.T) {
		h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "tok-value"}})
		head := h.repo.Commit("main", map[string]string{".plumbix/pipelines/ci.yaml": secretPipeline}, "ci")
		runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", After: head}})
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %+v, %v", runs, err)
		}
		h.wait(runs[0].ID)
		if env, ran := jobEnv(h, "uses-token"); !ran || env["TOKEN"] != "tok-value" {
			t.Errorf("env = %v, ran %v", env, ran)
		}
	})
	t.Run("triggered on a feature branch", func(t *testing.T) {
		h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "tok-value"}})
		head := h.repo.Commit("feat", map[string]string{".plumbix/pipelines/ci.yaml": secretPipeline}, "ci")
		runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "feat", After: head}})
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %+v, %v", runs, err)
		}
		withheld(t, h, h.wait(runs[0].ID))
	})
	t.Run("plain branch run", func(t *testing.T) {
		h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "tok-value"}})
		h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps:\n  - {name: uses-token, image: x, run: x, secrets: [TOKEN]}\n"}, "secret step")
		r, err := h.eng.RunBranch(ctx, h.repoID, "feat")
		if err != nil {
			t.Fatal(err)
		}
		withheld(t, h, h.wait(r.ID))
	})
	t.Run("change run", func(t *testing.T) {
		h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "tok-value"}})
		h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps:\n  - {name: uses-token, image: x, run: x, secrets: [TOKEN]}\n  - {name: validate, image: \"plumbix-reference-validator:dev\", validator: true, needs: [uses-token]}\n"}, "secret step")
		withheld(t, h, h.validate(h.submit("feat").ID))
	})
}

func withheld(t *testing.T, h *harness, r run.Run) {
	t.Helper()
	for _, s := range r.Steps {
		if s.Name == "uses-token" {
			if s.Status != "errored" || !strings.Contains(s.Error, "protected branches") || strings.Contains(s.Error, "tok-value") {
				t.Errorf("step = %+v, want errored: withheld off protected branches", s)
			}
			if _, ran := jobEnv(h, "uses-token"); ran {
				t.Error("the step ran without its secret")
			}
			return
		}
	}
	t.Fatalf("no uses-token step in %+v", r.Steps)
}
