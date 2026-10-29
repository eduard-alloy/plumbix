package engine

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/guard"
)

// clone is a working clone of the harness remote, for merges made the way
// GitHub makes them: outside Plumbix.
type clone struct {
	t   *testing.T
	dir string
}

func (h *harness) clone() *clone {
	h.t.Helper()
	dir := filepath.Join(h.t.TempDir(), "clone")
	c := &clone{t: h.t, dir: dir}
	if out, err := exec.Command("git", "clone", "-q", h.repo.Remote, dir).CombinedOutput(); err != nil {
		h.t.Fatalf("clone: %v %s", err, out)
	}
	c.git("config", "user.name", "Maintainer")
	c.git("config", "user.email", "maintainer@example.com")
	return c
}

func (c *clone) git(args ...string) string {
	c.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = c.dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=Maintainer", "GIT_AUTHOR_EMAIL=maintainer@example.com", "GIT_COMMITTER_NAME=Maintainer", "GIT_COMMITTER_EMAIL=maintainer@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// prChange submits a change that mirrors pull request number on branch.
func (h *harness) prChange(branch string, number int) change.Change {
	h.t.Helper()
	c, err := h.svc.Submit(ctx, change.ChangeInput{
		RepoID: h.repoID, SourceBranch: branch, TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}}},
		PullRequest:        &change.PullRequestRef{Number: number, URL: "https://github.com/a/b/pull/1"},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

// TestMergeRefusedWhenGitHubMerges: on a repository GitHub merges, a
// pull-request change is not pushed by Plumbix; a change without a pull
// request still is.
func TestMergeRefusedWhenGitHubMerges(t *testing.T) {
	h := newHarness(t, Config{})
	setMerger(t, h, change.MergerGitHub)
	h.repo.Commit("feat", map[string]string{"feat.go": "package x\n"}, "work")
	c := h.prChange("feat", 1)
	if r := h.validate(c.ID); r.Outcome != change.Approved {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	_, err := h.eng.Merge(ctx, c.ID)
	if !errors.Is(err, ErrGitHubMerges) || !strings.Contains(err.Error(), "pull request") {
		t.Fatalf("merge of a PR change: %v, want ErrGitHubMerges", err)
	}
	plain := h.approve("other")
	if _, err := h.eng.Merge(ctx, plain.ID); err != nil {
		t.Fatalf("merge of a plain change on a GitHub-merged repository: %v", err)
	}
}

// TestRecordExternalMerge: a merge GitHub made is recorded with the
// target's previous head worked out from the merge commit, for each of
// GitHub's three merge methods, and the change moves to merged.
func TestRecordExternalMerge(t *testing.T) {
	for _, method := range []string{"merge", "squash", "rebase"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t, Config{})
			setMerger(t, h, change.MergerGitHub)
			oldMain := h.remote("rev-parse", "main")
			h.repo.Commit("feat", map[string]string{"one.go": "package x\n"}, "first commit")
			head := h.repo.Commit("feat", map[string]string{"two.go": "package x\n"}, "second commit")
			c := h.prChange("feat", 1)
			if r := h.validate(c.ID); r.Outcome != change.Approved {
				t.Fatalf("outcome = %s", r.Outcome)
			}
			w := h.clone()
			w.git("checkout", "-q", "main")
			switch method {
			case "merge":
				w.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1", "origin/feat")
			case "squash":
				w.git("merge", "--squash", "-q", "origin/feat")
				w.git("commit", "-q", "-m", "t (#1)")
			case "rebase":
				w.git("cherry-pick", "main..origin/feat")
			}
			w.git("push", "-q", "origin", "main")
			merged := w.git("rev-parse", "main")

			got, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: merged, HeadSHA: head, Commits: 2, MergedBy: "maintainer"})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != change.Merged || got.Merge == nil || got.Merge.Commit != merged || got.Merge.Previous != oldMain || got.Merge.Strategy != change.StrategyGitHub || got.Merge.MergedBy != "maintainer" {
				t.Fatalf("%s: change = %s %+v, want previous %s", method, got.State, got.Merge, short(oldMain))
			}
			if _, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: merged}); !errors.Is(err, change.ErrInvalidState) {
				t.Errorf("second record: %v", err)
			}
		})
	}
}

// TestRecordExternalMergeOfUnapprovedChange: GitHub may merge before the
// gate agreed; the merge is still recorded (it happened) and the change
// says it is merged, whatever its state was.
func TestRecordExternalMergeOfUnapprovedChange(t *testing.T) {
	h := newHarness(t, Config{})
	setMerger(t, h, change.MergerGitHub)
	head := h.repo.Commit("feat", map[string]string{"one.go": "package x\n"}, "first")
	c := h.prChange("feat", 1)
	w := h.clone()
	w.git("checkout", "-q", "main")
	w.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1", "origin/feat")
	w.git("push", "-q", "origin", "main")
	merged := w.git("rev-parse", "main")
	got, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: merged, HeadSHA: head, Commits: 1, MergedBy: "maintainer"})
	if err != nil || got.State != change.Merged || got.Merge == nil || got.Merge.RunID != "" {
		t.Fatalf("change = %+v, %v", got, err)
	}
}

// setMerger flips the harness repository to GitHub-merged.
func setMerger(t *testing.T, h *harness, merger string) {
	t.Helper()
	if err := h.db.UpdateRepoMerging(ctx, h.repoID, true, merger); err != nil {
		t.Fatal(err)
	}
}

// TestExternalMergePastTheGateApprovesNoPipeline: when GitHub merged a
// pull request whose change was not approved, the pipeline files it
// brought are not recorded as approved (A38), and pipeline_unapproved says
// so; an approved change's files are recorded as on a Plumbix merge.
func TestExternalMergePastTheGateApprovesNoPipeline(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "bypassed", true: "approved"}[approved], func(t *testing.T) {
			h := newHarness(t, Config{})
			setMerger(t, h, change.MergerGitHub)
			edited := pipelineYAML + "  - {name: extra, image: x, run: x}\n"
			head := h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": edited}, "edit the pipeline")
			c := h.prChange("feat", 1)
			if approved {
				if r := h.validate(c.ID); r.Outcome != change.NeedsHuman {
					t.Fatalf("outcome = %s, want needs_human for a pipeline edit", r.Outcome)
				}
				if _, err := h.eng.ApproveByPerson(ctx, c.ID, tester, "reviewed the pipeline"); err != nil {
					t.Fatal(err)
				}
			}
			w := h.clone()
			w.git("checkout", "-q", "main")
			w.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1", "origin/feat")
			w.git("push", "-q", "origin", "main")
			merged := w.git("rev-parse", "main")
			if _, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: merged, HeadSHA: head, Commits: 1, MergedBy: "maintainer"}); err != nil {
				t.Fatal(err)
			}
			ok, err := h.db.PipelineApproved(ctx, h.repoID, PipelineFile, guard.Digest([]byte(edited)))
			if err != nil {
				t.Fatal(err)
			}
			if ok != approved {
				t.Errorf("pipeline file approved = %v, want %v", ok, approved)
			}
			alerts, _ := h.db.List(ctx, false)
			var unapproved int
			for _, a := range alerts {
				if a.Kind == alert.PipelineUnapproved {
					unapproved++
				}
			}
			if (unapproved > 0) == approved {
				t.Errorf("pipeline_unapproved alerts = %d, approved = %v", unapproved, approved)
			}
		})
	}
}

// TestExternalMergeApprovalMustCoverTheHead: an approval stands for one
// head. When GitHub merges a later commit before Plumbix judged it (the
// gate was not required), the pipeline files are not recorded as approved.
func TestExternalMergeApprovalMustCoverTheHead(t *testing.T) {
	h := newHarness(t, Config{})
	setMerger(t, h, change.MergerGitHub)
	h.repo.Commit("feat", map[string]string{"one.go": "package x\n"}, "first")
	c := h.prChange("feat", 1)
	if r := h.validate(c.ID); r.Outcome != change.Approved {
		t.Fatalf("outcome = %s", r.Outcome)
	}
	edited := pipelineYAML + "  - {name: extra, image: x, run: x}\n"
	later := h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": edited}, "pipeline edit after the approval")
	w := h.clone()
	w.git("checkout", "-q", "main")
	w.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1", "origin/feat")
	w.git("push", "-q", "origin", "main")
	merged := w.git("rev-parse", "main")
	got, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: merged, HeadSHA: later, Commits: 2, MergedBy: "maintainer"})
	if err != nil || got.State != change.Merged {
		t.Fatalf("change = %+v, %v", got, err)
	}
	if ok, _ := h.db.PipelineApproved(ctx, h.repoID, PipelineFile, guard.Digest([]byte(edited))); ok {
		t.Error("a pipeline file nobody validated was recorded as approved")
	}
	alerts, _ := h.db.List(ctx, false)
	if len(alerts) != 1 || alerts[0].Kind != alert.PipelineUnapproved {
		t.Errorf("alerts = %+v", alerts)
	}
}

// TestUnapprovedExternalMergeWithoutPipelineFilesIsQuiet: a merge past the
// gate that touches no pipeline file raises no pipeline_unapproved alert;
// gate_bypassed is the intake's business.
func TestUnapprovedExternalMergeWithoutPipelineFilesIsQuiet(t *testing.T) {
	h := newHarness(t, Config{})
	setMerger(t, h, change.MergerGitHub)
	head := h.repo.Commit("feat", map[string]string{"one.go": "package x\n"}, "first")
	c := h.prChange("feat", 1)
	w := h.clone()
	w.git("checkout", "-q", "main")
	w.git("merge", "--no-ff", "-q", "-m", "Merge pull request #1", "origin/feat")
	w.git("push", "-q", "origin", "main")
	if _, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: w.git("rev-parse", "main"), HeadSHA: head, Commits: 1}); err != nil {
		t.Fatal(err)
	}
	if alerts, _ := h.db.List(ctx, false); len(alerts) != 0 {
		t.Errorf("alerts = %+v", alerts)
	}
}

// TestExternalMergeRecordsAutoMerge: the flag reaches the change's merge
// record and the change.merged payload (WU-063).
func TestExternalMergeRecordsAutoMerge(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "x\n"}, "feature")
	c := h.submit("feat")
	head := h.remote("rev-parse", "feat")
	landed := h.repo.Commit("main", map[string]string{"a.go": "x\n"}, "squash of feat")
	got, err := h.eng.RecordExternalMerge(ctx, c.ID, ExternalMerge{Commit: landed, HeadSHA: head, Commits: 1, MergedBy: "eduard", AutoMerge: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Merge == nil || !got.Merge.AutoMerge || got.Merge.MergedBy != "eduard" {
		t.Fatalf("merge = %+v", got.Merge)
	}
	evs, _ := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	var payload events.ChangeMergedPayload
	for _, e := range evs {
		if e.Type == events.ChangeMerged {
			raw, _ := json.Marshal(e.Payload)
			_ = json.Unmarshal(raw, &payload)
		}
	}
	if !payload.AutoMerge || payload.MergedBy != "eduard" {
		t.Errorf("payload = %+v", payload)
	}
}
