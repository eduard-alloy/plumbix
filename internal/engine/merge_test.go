package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// approve submits a change on branch and validates it to approval.
func (h *harness) approve(branch string) change.Change {
	h.t.Helper()
	h.repo.Commit(branch, map[string]string{branch + ".go": "package x\n"}, "work on "+branch)
	c := h.submit(branch)
	if r := h.validate(c.ID); r.Outcome != change.Approved {
		h.t.Fatalf("outcome = %s (%s)", r.Outcome, r.Error)
	}
	return c
}

// remote runs git in the test remote and returns trimmed stdout.
func (h *harness) remote(args ...string) string {
	h.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", h.repo.Remote}, args...)...).Output()
	if err != nil {
		h.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func (h *harness) merge(id string) (change.Change, error) { return h.eng.Merge(ctx, id) }

func TestMergeStrategies(t *testing.T) {
	for _, strategy := range []string{"squash", "merge", "rebase"} {
		t.Run(strategy, func(t *testing.T) {
			h := newHarnessWith(t, Config{}, strategy)
			oldMain := h.remote("rev-parse", "main")
			c := h.approve("feat")
			head := h.remote("rev-parse", "feat")

			merged, err := h.merge(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			newMain := h.remote("rev-parse", "main")
			if merged.State != change.Merged || merged.Merge == nil || merged.Merge.Commit != newMain || merged.Merge.Previous != oldMain || merged.Merge.Strategy != strategy {
				t.Fatalf("merged = %s %+v, main = %s", merged.State, merged.Merge, newMain)
			}
			if h.remote("rev-parse", "main^{tree}") != h.remote("rev-parse", head+"^{tree}") {
				t.Error("main does not hold the validated tree")
			}
			parents := strings.Fields(h.remote("log", "-1", "--format=%P", "main"))
			msg := h.remote("log", "-1", "--format=%B", "main")
			switch strategy {
			case "squash":
				if len(parents) != 1 || parents[0] != oldMain || !strings.Contains(msg, "Plumbix-Change: "+c.ID) || !strings.HasPrefix(msg, "t\n\ni\n") {
					t.Errorf("squash: parents %v, message %q", parents, msg)
				}
			case "merge":
				if len(parents) != 2 || parents[0] != oldMain || parents[1] != head || !strings.Contains(msg, "Plumbix-Run: "+merged.Merge.RunID) {
					t.Errorf("merge: parents %v, message %q", parents, msg)
				}
			case "rebase":
				if newMain != head {
					t.Errorf("rebase: main = %s, want %s", newMain, head)
				}
			}
			if _, err := h.merge(c.ID); !errors.Is(err, change.ErrInvalidState) {
				t.Errorf("second merge err = %v", err)
			}
		})
	}
}

func TestMergeRefusals(t *testing.T) {
	t.Run("not approved", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
		c := h.submit("feat")
		if _, err := h.merge(c.ID); !errors.Is(err, change.ErrInvalidState) {
			t.Errorf("err = %v", err)
		}
		if _, err := h.merge("c_missing00000"); !errors.Is(err, change.ErrNotFound) {
			t.Errorf("missing err = %v", err)
		}
	})
	cases := map[string]struct {
		move func(h *harness)
		want error
	}{
		"source moved": {func(h *harness) { h.repo.Commit("feat", map[string]string{"more.go": "x\n"}, "more") }, ErrSourceMoved},
		"target moved": {func(h *harness) { h.repo.Commit("main", map[string]string{"hot.go": "x\n"}, "hotfix") }, ErrTargetMoved},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Config{})
			c := h.approve("feat")
			tc.move(h)
			before := h.remote("rev-parse", "main")
			if _, err := h.merge(c.ID); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v", err)
			}
			if h.remote("rev-parse", "main") != before || h.state(c.ID) != change.Approved {
				t.Error("remote or change touched by a refused merge")
			}
		})
	}
	t.Run("remote moved during merge", func(t *testing.T) {
		h := newHarness(t, Config{})
		c := h.approve("feat")
		var hotfix string
		h.eng.beforePush = func() { hotfix = h.repo.Commit("main", map[string]string{"hot.go": "x\n"}, "hotfix") }
		if _, err := h.merge(c.ID); !errors.Is(err, gitops.ErrPushRejected) {
			t.Fatalf("err = %v", err)
		}
		got, _ := h.db.GetChange(ctx, c.ID)
		if h.remote("rev-parse", "main") != hotfix || got.State != change.Approved || got.Merge != nil {
			t.Errorf("after rejected push: main=%s state=%s merge=%+v", h.remote("rev-parse", "main"), got.State, got.Merge)
		}
	})
}

func TestValidateRefusedDuringMerge(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.approve("feat")
	var during error
	h.eng.beforePush = func() { _, during = h.eng.Validate(ctx, c.ID) }
	if _, err := h.merge(c.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(during, change.ErrInvalidState) {
		t.Errorf("validate during merge: %v", during)
	}
	if h.state(c.ID) != change.Merged {
		t.Errorf("state = %s", h.state(c.ID))
	}
}

func TestCommitsFetchesFirst(t *testing.T) {
	h := newHarness(t, Config{})
	a := h.remote("rev-parse", "main")
	b := h.repo.Commit("main", map[string]string{"b.txt": "b\n"}, "second")
	c := h.repo.Commit("main", map[string]string{"c.txt": "c\n"}, "third")
	repo, _ := h.db.GetRepo(ctx, h.repoID)
	got, err := h.eng.Commits(ctx, repo, a, c)
	if err != nil || len(got) != 2 || got[0].SHA != c || got[1].SHA != b || got[0].Author != "Test" {
		t.Fatalf("commits = %+v, %v", got, err)
	}
}

// fakeLanding stands in for the pull-request package's Lander: it answers
// whether the gate is required and records what PostGate was asked to
// post, checking that the holding ref already sits on the remote at that
// commit when it is called.
type fakeLanding struct {
	h               *harness
	required, known bool
	err, postErr    error
	onPost          func() // runs after the gate is recorded, before PostGate returns
	posted          []string
	refAtPost       []bool
}

func (f *fakeLanding) GateRequired(context.Context, change.Repo, string) (bool, bool, error) {
	return f.required, f.known, f.err
}

func (f *fakeLanding) PostGate(_ context.Context, _ change.Repo, c change.Change, _ run.Run, sha string) error {
	f.posted = append(f.posted, sha)
	out, err := exec.Command("git", "-C", f.h.repo.Remote, "rev-parse", "--verify", "--quiet", "refs/plumbix/landing/"+c.ID).Output()
	f.refAtPost = append(f.refAtPost, err == nil && strings.TrimSpace(string(out)) == sha)
	if f.onPost != nil {
		f.onPost()
	}
	return f.postErr
}

func (h *harness) holdingRef(id string) (string, bool) {
	out, err := exec.Command("git", "-C", h.repo.Remote, "rev-parse", "--verify", "--quiet", "refs/plumbix/landing/"+id).Output()
	return strings.TrimSpace(string(out)), err == nil
}

// TestMergeLandsThroughTheGate: when the target requires plumbix/gate,
// the merge pushes the landing commit to a holding ref, posts the gate on
// it, then moves the target and drops the holding ref; when the gate is
// not required or the requirement is unknown, it pushes as before. A gate
// that cannot be posted stops the merge before the target moves (WU-059).
func TestMergeLandsThroughTheGate(t *testing.T) {
	t.Run("required", func(t *testing.T) {
		h := newHarnessWith(t, Config{}, "squash")
		l := &fakeLanding{h: h, required: true, known: true}
		h.eng.SetLanding(l)
		c := h.approve("feat")
		merged, err := h.merge(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		newMain := h.remote("rev-parse", "main")
		if len(l.posted) != 1 || l.posted[0] != newMain || !l.refAtPost[0] {
			t.Errorf("posted %v (ref present at post: %v); main = %s", l.posted, l.refAtPost, newMain)
		}
		if _, ok := h.holdingRef(c.ID); ok {
			t.Error("the holding ref outlived the merge")
		}
		if merged.State != change.Merged || merged.Merge == nil || merged.Merge.Commit != newMain {
			t.Errorf("merged = %s %+v", merged.State, merged.Merge)
		}
	})
	for name, l := range map[string]*fakeLanding{"not required": {required: false, known: true}, "unknown": {required: false, known: false}, "read failed": {err: errors.New("GitHub status 500")}} {
		t.Run(name, func(t *testing.T) {
			h := newHarnessWith(t, Config{}, "squash")
			l.h = h
			h.eng.SetLanding(l)
			c := h.approve("feat")
			if _, err := h.merge(c.ID); err != nil {
				t.Fatal(err)
			}
			if len(l.posted) != 0 {
				t.Errorf("gate posted: %v", l.posted)
			}
			if _, ok := h.holdingRef(c.ID); ok {
				t.Error("a holding ref was pushed")
			}
		})
	}
	t.Run("target refused by rules after the gate was posted", func(t *testing.T) {
		h := newHarnessWith(t, Config{}, "squash")
		// The remote refuses branch updates the way GitHub's rulesets do, but lets the holding ref through.
		hook := filepath.Join(h.repo.Remote, "hooks", "pre-receive")
		script := "#!/bin/sh\nwhile read old new ref; do\n  case \"$ref\" in refs/heads/main) echo 'error: GH013: Repository rule violations found for refs/heads/main.' >&2; echo '- Required status check \"other/check\" is expected.' >&2; exit 1;; esac\ndone\nexit 0\n"
		if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		l := &fakeLanding{h: h, required: true, known: true}
		h.eng.SetLanding(l)
		oldMain := h.remote("rev-parse", "main")
		c := h.approve("feat")
		_, err := h.merge(c.ID)
		if !errors.Is(err, gitops.ErrPushRefused) || !strings.Contains(err.Error(), `Required status check "other/check" is expected`) || !strings.Contains(err.Error(), "Plumbix posted its gate") {
			t.Fatalf("err = %v", err)
		}
		if len(l.posted) != 1 || !l.refAtPost[0] {
			t.Errorf("gate posted %v (ref present: %v)", l.posted, l.refAtPost)
		}
		if h.remote("rev-parse", "main") != oldMain {
			t.Error("main moved")
		}
		if _, ok := h.holdingRef(c.ID); ok {
			t.Error("the holding ref was left behind after the refusal")
		}
		if got, _ := h.db.GetChange(ctx, c.ID); got.State != change.Approved {
			t.Errorf("state = %s", got.State)
		}
	})
	t.Run("target moved while the gate was posted", func(t *testing.T) {
		h := newHarnessWith(t, Config{}, "squash")
		l := &fakeLanding{h: h, required: true, known: true}
		l.onPost = func() { h.repo.Commit("main", map[string]string{"late.txt": "late\n"}, "late push to main") }
		h.eng.SetLanding(l)
		c := h.approve("feat")
		if _, err := h.merge(c.ID); !errors.Is(err, gitops.ErrPushRejected) {
			t.Fatalf("err = %v, want the moved-branch rejection", err)
		}
		if _, ok := h.holdingRef(c.ID); ok {
			t.Error("the holding ref was left behind after the rejection")
		}
	})
	t.Run("gate cannot be posted", func(t *testing.T) {
		h := newHarnessWith(t, Config{}, "squash")
		l := &fakeLanding{h: h, required: true, known: true, postErr: errors.New("GitHub status 422")}
		h.eng.SetLanding(l)
		oldMain := h.remote("rev-parse", "main")
		c := h.approve("feat")
		_, err := h.merge(c.ID)
		if err == nil || !strings.Contains(err.Error(), "GitHub status 422") {
			t.Fatalf("err = %v", err)
		}
		if h.remote("rev-parse", "main") != oldMain {
			t.Error("main moved although the gate was not posted")
		}
		if _, ok := h.holdingRef(c.ID); ok {
			t.Error("the holding ref was left behind")
		}
		if got, _ := h.db.GetChange(ctx, c.ID); got.State != change.Approved {
			t.Errorf("state = %s, want approved so the merge can be tried again", got.State)
		}
	})
}
