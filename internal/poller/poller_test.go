package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/store"
)

var ctx = context.Background()

// fakeEngine serves scripted remote heads and records triggers.
type fakeEngine struct {
	mu        sync.Mutex
	heads     map[string]map[string]string // repo ID -> branch -> sha
	failRepo  string
	history   map[string]gitops.Commit
	validated []string
	branches  []string
}

func (f *fakeEngine) ListHeads(_ context.Context, repo change.Repo) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo.ID == f.failRepo {
		return nil, errors.New("remote unreachable")
	}
	out := map[string]string{}
	for b, h := range f.heads[repo.ID] {
		out[b] = h
	}
	return out, nil
}

func (f *fakeEngine) Validate(_ context.Context, changeID string) (run.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validated = append(f.validated, changeID)
	return run.Run{}, nil
}

func (f *fakeEngine) RunBranch(_ context.Context, repoID, branch string) (run.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches = append(f.branches, repoID+"/"+branch)
	return run.Run{}, nil
}

func (f *fakeEngine) push(repo, branch, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.heads[repo] == nil {
		f.heads[repo] = map[string]string{}
	}
	if sha == "" {
		delete(f.heads[repo], branch)
		return
	}
	f.heads[repo][branch] = sha
}

func (f *fakeEngine) triggers() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, b := append([]string(nil), f.validated...), append([]string(nil), f.branches...)
	f.validated, f.branches = nil, nil
	sort.Strings(b)
	return v, b
}

type harness struct {
	db  *store.DB
	eng *fakeEngine
	p   *Poller
}

func newHarness(t *testing.T, repos ...string) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i, id := range repos {
		if err := db.CreateRepo(ctx, change.Repo{ID: id, Host: "github", URL: "https://github.com/a/r" + string(rune('0'+i)), ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	eng := &fakeEngine{heads: map[string]map[string]string{}}
	return &harness{db: db, eng: eng, p: New(db, eng, nil, Config{Interval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))}
}

func (h *harness) change(t *testing.T, repoID, branch string) change.Change {
	t.Helper()
	c := change.Change{ID: change.NewID("c_"), RepoID: repoID, SourceBranch: branch, TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "x"}}, Constraints: []string{}, State: change.Open, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFirstPollOnlyRecords(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.push("r_1", "main", "m1")
	h.eng.push("r_1", "feat", "f1")
	if err := h.p.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if v, b := h.eng.triggers(); len(v)+len(b) != 0 {
		t.Fatalf("first poll triggered %v %v", v, b)
	}
	heads, polled, _ := h.db.KnownHeads(ctx, "r_1")
	if !polled || heads["feat"] != "f1" {
		t.Errorf("heads = %v polled %v", heads, polled)
	}
}

func TestPushesTriggerTheRightRuns(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.push("r_1", "main", "m1")
	h.eng.push("r_1", "feat", "f1")
	c := h.change(t, "r_1", "feat")
	_ = h.p.PollOnce(ctx)
	h.eng.triggers()

	h.eng.push("r_1", "feat", "f2")   // change branch moved
	h.eng.push("r_1", "main", "m2")   // other branch moved
	h.eng.push("r_1", "new-ci", "n1") // new branch without a change
	if err := h.p.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	v, b := h.eng.triggers()
	if len(v) != 1 || v[0] != c.ID {
		t.Errorf("validated = %v, want the change", v)
	}
	if len(b) != 2 || b[0] != "r_1/main" || b[1] != "r_1/new-ci" {
		t.Errorf("plain runs = %v", b)
	}

	if err := h.p.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if v, b := h.eng.triggers(); len(v)+len(b) != 0 {
		t.Errorf("unchanged heads triggered %v %v", v, b)
	}
}

func TestChangeAlreadyAtHeadIsNotRevalidated(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.push("r_1", "feat", "f1")
	c := h.change(t, "r_1", "feat")
	_ = h.p.PollOnce(ctx)
	h.eng.push("r_1", "feat", "f2")
	if err := h.db.CreateRun(ctx, run.Run{ID: "run_aaaaaaaaaaaa", RepoID: "r_1", ChangeID: c.ID, Branch: "feat", Iteration: 1, Head: "f2", Status: run.Finished, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = h.p.PollOnce(ctx)
	if v, b := h.eng.triggers(); len(v)+len(b) != 0 {
		t.Errorf("triggered %v %v although the change was validated at f2", v, b)
	}
}

func TestRunningRunWithoutHeadIsLeftAlone(t *testing.T) {
	h := newHarness(t, "r_1")
	_ = h.p.PollOnce(ctx)
	c := h.change(t, "r_1", "feat")
	if err := h.db.CreateRun(ctx, run.Run{ID: "run_bbbbbbbbbbbb", RepoID: "r_1", ChangeID: c.ID, Branch: "feat", Iteration: 1, Status: run.Running, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.eng.push("r_1", "feat", "f1")
	_ = h.p.PollOnce(ctx)
	if v, _ := h.eng.triggers(); len(v) != 0 {
		t.Errorf("revalidated %v while a run was about to fetch the branch", v)
	}
}

func TestClosedChangeBranchGetsPlainRun(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.push("r_1", "feat", "f1")
	c := h.change(t, "r_1", "feat")
	_ = h.p.PollOnce(ctx)
	if err := h.db.SetChangeState(ctx, c.ID, change.Open, change.Closed, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.eng.push("r_1", "feat", "f2")
	_ = h.p.PollOnce(ctx)
	v, b := h.eng.triggers()
	if len(v) != 0 || len(b) != 1 {
		t.Errorf("validated %v, plain %v", v, b)
	}
}

func TestDeletedBranchesAndFailingRepos(t *testing.T) {
	h := newHarness(t, "r_1", "r_2")
	h.eng.push("r_1", "gone", "g1")
	h.eng.push("r_2", "main", "m1")
	_ = h.p.PollOnce(ctx)
	h.eng.push("r_1", "gone", "")
	h.eng.failRepo = "r_2"
	err := h.p.PollOnce(ctx)
	if err == nil {
		t.Error("expected an error reporting the failing repo")
	}
	heads, _, _ := h.db.KnownHeads(ctx, "r_1")
	if _, still := heads["gone"]; still {
		t.Error("deleted branch still remembered")
	}
	heads2, _, _ := h.db.KnownHeads(ctx, "r_2")
	if heads2["main"] != "m1" {
		t.Error("failing repo's known heads were lost")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	h := newHarness(t, "r_1")
	h.p.cfg.Interval = 10 * time.Millisecond
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { h.p.Run(c); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if _, polled, _ := h.db.KnownHeads(ctx, "r_1"); !polled {
		t.Error("Run never polled")
	}
}

// TestHumanStatesDoNotRevalidateOnPush: a change that needs a person stays
// where it is when its branch moves; only Validate again or Close move it.
// The other open states still revalidate, and running runs are superseded.
func TestHumanStatesDoNotRevalidateOnPush(t *testing.T) {
	sticky := []change.State{change.NeedsHuman, change.Escalated, change.Rejected}
	live := []change.State{change.Open, change.FixRequested, change.Approved, change.Running}
	h := newHarness(t, "r_1")
	h.eng.push("r_1", "main", "m1")
	ids := map[change.State]string{}
	for i, st := range append(append([]change.State{}, sticky...), live...) {
		branch := "b" + string(rune('a'+i))
		h.eng.push("r_1", branch, branch+"1")
		c := h.change(t, "r_1", branch)
		if st != change.Open {
			if err := h.db.SetChangeState(ctx, c.ID, change.Open, st, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		ids[st] = c.ID
	}
	_ = h.p.PollOnce(ctx)
	h.eng.triggers()
	for i := range sticky {
		h.eng.push("r_1", "b"+string(rune('a'+i)), "moved")
	}
	for i := range live {
		h.eng.push("r_1", "b"+string(rune('a'+len(sticky)+i)), "moved")
	}
	if err := h.p.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	v, b := h.eng.triggers()
	if len(b) != 0 {
		t.Errorf("plain runs %v for branches that have a change", b)
	}
	got := map[string]bool{}
	for _, id := range v {
		got[id] = true
	}
	for _, st := range sticky {
		if got[ids[st]] {
			t.Errorf("%s was revalidated by a push", st)
		}
	}
	for _, st := range live {
		if !got[ids[st]] {
			t.Errorf("%s was not revalidated by a push", st)
		}
	}
	// The poller still records the new heads, so the change can show them.
	if c, _ := h.db.GetChange(ctx, ids[change.NeedsHuman]); c.SourceHead != "moved" {
		t.Errorf("needs_human change head = %q, want the pushed head", c.SourceHead)
	}
}
