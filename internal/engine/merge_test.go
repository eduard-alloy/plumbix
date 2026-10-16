package engine

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
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
