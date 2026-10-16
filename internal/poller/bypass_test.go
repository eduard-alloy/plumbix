package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// commit registers a commit in the fake engine's history.
func (f *fakeEngine) commit(sha, parent, author string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.history == nil {
		f.history = map[string]gitops.Commit{}
	}
	c := gitops.Commit{SHA: sha, Author: author, Email: author + "@example.com", Time: time.Now(), Subject: "commit " + sha}
	if parent != "" {
		c.Parents = []string{parent}
	}
	f.history[sha] = c
}

// Commits walks first parents from to back to from.
func (f *fakeEngine) Commits(_ context.Context, _ change.Repo, from, to string) ([]gitops.Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gitops.Commit
	for sha := to; sha != "" && sha != from; {
		c, ok := f.history[sha]
		if !ok {
			return nil, errors.New("unknown commit " + sha)
		}
		out = append(out, c)
		if from == "" || len(c.Parents) == 0 {
			break
		}
		sha = c.Parents[0]
	}
	return out, nil
}

// merged records a Plumbix merge of commit onto previous on main.
func (h *harness) merged(t *testing.T, repoID, branch, commit, previous string) {
	t.Helper()
	id := change.NewID("c_")
	c := change.Change{ID: id, RepoID: repoID, SourceBranch: "src-" + commit, TargetBranch: branch, Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "x"}}, Constraints: []string{}, State: change.Merged, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := run.Run{ID: change.NewID("run_"), RepoID: repoID, ChangeID: id, Branch: c.SourceBranch, Iteration: 1, Status: run.Running, StartedAt: time.Now()}
	if err := h.db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := h.db.RecordMerge(ctx, change.MergeRecord{ChangeID: id, RepoID: repoID, Branch: branch, Merge: change.Merge{Commit: commit, Previous: previous, Strategy: "squash", RunID: r.ID, MergedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) openAlerts(t *testing.T) []alert.Alert {
	t.Helper()
	as, err := h.db.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func TestForeignCommitOnProtectedBranchRaisesAlert(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.commit("a", "", "Alice")
	h.eng.push("r_1", "main", "a")
	_ = h.p.PollOnce(ctx)

	h.eng.commit("b", "a", "Mallory")
	h.eng.push("r_1", "main", "b")
	_ = h.p.PollOnce(ctx)
	as := h.openAlerts(t)
	if len(as) != 1 || as[0].Kind != alert.BypassCommit || as[0].Key != "main@b" || as[0].RepoID != "r_1" {
		t.Fatalf("alerts = %+v", as)
	}
	commits := as[0].Details["commits"].([]any)
	if len(commits) != 1 || commits[0].(map[string]any)["sha"] != "b" || commits[0].(map[string]any)["author"] != "Mallory" || as[0].Details["branch"] != "main" {
		t.Errorf("details = %v", as[0].Details)
	}
	if _, b := h.eng.triggers(); len(b) != 1 {
		t.Errorf("foreign commit should still get a plain run: %v", b)
	}
	// Nothing new: still one alert. A further foreign commit: a second one.
	_ = h.p.PollOnce(ctx)
	if len(h.openAlerts(t)) != 1 {
		t.Error("unchanged head raised another alert")
	}
	h.eng.commit("c", "b", "Mallory")
	h.eng.push("r_1", "main", "c")
	_ = h.p.PollOnce(ctx)
	if as := h.openAlerts(t); len(as) != 2 || as[0].Key != "main@c" {
		t.Errorf("alerts = %+v", as)
	}
	// An unprotected branch never raises one.
	h.eng.commit("f", "a", "Mallory")
	h.eng.push("r_1", "feat", "f")
	_ = h.p.PollOnce(ctx)
	if len(h.openAlerts(t)) != 2 {
		t.Error("unprotected branch raised an alert")
	}
}

func TestPlumbixMergesAreNotBypasses(t *testing.T) {
	h := newHarness(t, "r_1")
	h.eng.commit("a", "", "Alice")
	h.eng.push("r_1", "main", "a")
	_ = h.p.PollOnce(ctx)

	// A squash merge: one commit, recorded.
	h.eng.commit("m1", "a", "Alice")
	h.merged(t, "r_1", "main", "m1", "a")
	h.eng.push("r_1", "main", "m1")
	_ = h.p.PollOnce(ctx)
	if as := h.openAlerts(t); len(as) != 0 {
		t.Fatalf("plumbix merge flagged: %+v", as)
	}
	if _, b := h.eng.triggers(); len(b) != 0 {
		t.Errorf("plumbix merge started a plain run: %v", b)
	}

	// A fast-forward of two commits, recorded as one range, plus a foreign
	// commit on top: only the foreign one is flagged.
	h.eng.commit("r1", "m1", "Alice")
	h.eng.commit("r2", "r1", "Alice")
	h.merged(t, "r_1", "main", "r2", "m1")
	h.eng.commit("x", "r2", "Mallory")
	h.eng.push("r_1", "main", "x")
	_ = h.p.PollOnce(ctx)
	as := h.openAlerts(t)
	if len(as) != 1 || as[0].Key != "main@x" {
		t.Fatalf("alerts = %+v", as)
	}
	if commits := as[0].Details["commits"].([]any); len(commits) != 1 || commits[0].(map[string]any)["sha"] != "x" {
		t.Errorf("flagged commits = %v", commits)
	}
	if _, b := h.eng.triggers(); len(b) != 1 {
		t.Errorf("foreign commit should get a plain run: %v", b)
	}
}

func TestNewProtectedBranchFlagsOnlyItsTip(t *testing.T) {
	h := newHarness(t)
	if err := h.db.CreateRepo(ctx, change.Repo{ID: "r_9", Host: "github", URL: "https://github.com/a/r9", ProtectedBranches: []string{"main", "release"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.eng.commit("a", "", "Alice")
	h.eng.commit("b", "a", "Alice")
	h.eng.push("r_9", "main", "b")
	_ = h.p.PollOnce(ctx)
	h.eng.commit("rel", "b", "Bob")
	h.eng.push("r_9", "release", "rel")
	_ = h.p.PollOnce(ctx)
	as := h.openAlerts(t)
	if len(as) != 1 || as[0].Key != "release@rel" {
		t.Fatalf("alerts = %+v", as)
	}
	if commits := as[0].Details["commits"].([]any); len(commits) != 1 {
		t.Errorf("new branch flagged its whole history: %v", commits)
	}
}

type fakeHost struct {
	exp time.Time
	ok  bool
	err error
}

func (fakeHost) Kind() string                                           { return "github" }
func (fakeHost) Auth(change.Repo) (gitops.Auth, bool)                   { return gitops.Auth{}, false }
func (f fakeHost) TokenExpiry(context.Context) (time.Time, bool, error) { return f.exp, f.ok, f.err }

func TestTokenExpiryAlerts(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fh := &fakeHost{exp: now.Add(3 * 24 * time.Hour), ok: true}
	hosts := host.Registry{"github": fh}
	p := New(h.db, h.eng, hosts, Config{Interval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.now = func() time.Time { return now }

	p.checkTokens(ctx)
	as := h.openAlerts(t)
	if len(as) != 1 || as[0].Kind != alert.TokenExpiry || as[0].Key != "github" || as[0].Details["expires"] == nil {
		t.Fatalf("alerts = %+v", as)
	}
	p.checkTokens(ctx)
	if len(h.openAlerts(t)) != 1 {
		t.Error("daily check duplicated the alert")
	}
	// Token renewed: the alert resolves itself.
	*fh = fakeHost{exp: now.Add(90 * 24 * time.Hour), ok: true}
	p.checkTokens(ctx)
	if len(h.openAlerts(t)) != 0 {
		t.Error("healthy token left the alert open")
	}
	// A failing check is an alert too.
	*fh = fakeHost{err: errors.New("rejected the token")}
	p.checkTokens(ctx)
	if as := h.openAlerts(t); len(as) != 1 || as[0].Message == "" {
		t.Errorf("alerts = %+v", as)
	}
}
