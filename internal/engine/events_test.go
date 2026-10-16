package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

func typesOf(evs []events.Event) []events.Type {
	out := make([]events.Type, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func sameTypes(a, b []events.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stepPayload(t *testing.T, e events.Event) events.StepFinishedPayload {
	t.Helper()
	data, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var p events.StepFinishedPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitState polls until the change reaches want, since the state moves
// right after the run is recorded as finished.
func (h *harness) waitState(id string, want change.State) {
	h.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if h.state(id) == want {
			return
		}
	}
	h.t.Fatalf("change %s is %s, want %s", id, h.state(id), want)
}

// TestChangeRunEmitsEventsInOrder checks the whole sequence one validation
// produces, including a step.finished per step, which only the engine knows.
func TestChangeRunEmitsEventsInOrder(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	h.wait(r.ID)
	h.waitState(c.ID, change.Approved)

	evs, err := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []events.Type{events.ChangeSubmitted, events.RunStarted, events.StateChanged, events.StepFinished, events.StepFinished, events.RunFinished, events.VerdictIssued, events.StateChanged}
	if got := typesOf(evs); !sameTypes(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
	for i, e := range evs {
		if e.RepoID != h.repoID || e.ChangeID != c.ID || (i > 0 && e.ID <= evs[i-1].ID) {
			t.Errorf("event %d = %+v; want repo %s, change %s, increasing ids", i, e, h.repoID, c.ID)
		}
		if e.Type != events.ChangeSubmitted && e.Type != events.StateChanged && e.RunID != r.ID {
			t.Errorf("event %s has run %q, want %s", e.Type, e.RunID, r.ID)
		}
	}
	test, validate := stepPayload(t, evs[3]), stepPayload(t, evs[4])
	if test.Step != "test" || test.Status != "succeeded" || test.Validator {
		t.Errorf("first step event = %+v", test)
	}
	if validate.Step != "validate" || validate.Status != "succeeded" || !validate.Validator {
		t.Errorf("second step event = %+v", validate)
	}
}

// TestPlainRunEmitsEvents: a push without a change still announces its run,
// with no change id and no verdict.
func TestPlainRunEmitsEvents(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("ci", map[string]string{"b.go": "package b\n"}, "push")
	r, err := h.eng.RunBranch(ctx, h.repoID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	h.wait(r.ID)
	all, err := h.db.EventsAfter(ctx, 0, events.Filter{RepoID: h.repoID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var evs []events.Event
	for _, e := range all {
		if e.RunID == r.ID {
			evs = append(evs, e)
		}
	}
	want := []events.Type{events.RunStarted, events.StepFinished, events.RunFinished}
	if got := typesOf(evs); !sameTypes(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
	for _, e := range evs {
		if e.ChangeID != "" {
			t.Errorf("plain run event %s names change %q", e.Type, e.ChangeID)
		}
	}
	if p := stepPayload(t, evs[1]); p.Step != "test" || p.Status != "succeeded" {
		t.Errorf("step event = %+v", p)
	}
}

// TestFailedStepEventCarriesTheFailure: the step event says what went wrong,
// so a client can show it before the run's summary exists.
func TestFailedStepEventCarriesTheFailure(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.exit["test"] = 2
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	h.wait(r.ID)
	evs, err := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var steps []events.StepFinishedPayload
	for _, e := range evs {
		if e.Type == events.StepFinished {
			steps = append(steps, stepPayload(t, e))
		}
	}
	if len(steps) != 2 || steps[0].Step != "test" || steps[0].Status != "failed" || steps[0].ExitCode != 2 || steps[1].Step != "validate" {
		t.Fatalf("step events = %+v; want test failed with exit 2, then the validator", steps)
	}
	_ = r
}

// TestStepsAreRecordedAsTheyFinish: a running run already lists the steps
// that ended, so a client can show progress before the verdict.
func TestStepsAreRecordedAsTheyFinish(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.block["validate"] = true
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got run.Run
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var err error
		if got, err = h.db.GetRun(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if len(got.Steps) > 0 {
			break
		}
	}
	if got.Status != run.Running || len(got.Steps) != 1 || got.Steps[0].Name != "test" || got.Steps[0].Status != "succeeded" {
		t.Fatalf("mid-run = %+v; want the finished test step while validate still runs", got)
	}
	// A blocked step only ends by cancellation: a new validation replaces
	// the run, and the finished run lists every step once, in order.
	h.fake.mu.Lock()
	h.fake.block["validate"] = false
	h.fake.mu.Unlock()
	done := h.wait(h.validate(c.ID).ID)
	if len(done.Steps) != 2 || done.Steps[0].Name != "test" || done.Steps[1].Name != "validate" || done.Steps[1].Status != "succeeded" {
		t.Fatalf("finished steps = %+v; want both once, in pipeline order", done.Steps)
	}
	if old := h.wait(r.ID); old.Status != run.Cancelled || len(old.Steps) != 2 {
		t.Fatalf("cancelled run = %+v; want its two steps (test succeeded, validate cancelled) once each", old)
	}
}

// fakeSettings stands in for the operator's settings.
type fakeSettings struct {
	global, repo, model string
}

func (f fakeSettings) Guidance(context.Context, string) (settings.Guidance, error) {
	return settings.Guidance{Global: f.global, Repository: f.repo}, nil
}
func (f fakeSettings) AIModel(context.Context) (string, error) { return f.model, nil }

// TestValidatorsGetGuidanceAndModel: validator steps see the operator's
// guidance in change.json and the model as PLUMBIX_AI_MODEL, unless the
// pipeline step sets its own; normal steps see neither.
func TestValidatorsGetGuidanceAndModel(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{global: "Prefer small diffs.", repo: "Go needs tests.", model: "claude-sonnet-5-5"}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if len(h.fake.inputs) != 1 {
		t.Fatalf("validator inputs = %d", len(h.fake.inputs))
	}
	g := h.fake.inputs[0].Guidance
	if g == nil || g.Global != "Prefer small diffs." || g.Repository != "Go needs tests." {
		t.Errorf("guidance = %+v", g)
	}
	for _, j := range h.fake.jobs {
		got := j.Env["PLUMBIX_AI_MODEL"]
		if j.Name == "validate" && got != "claude-sonnet-5-5" {
			t.Errorf("validator env = %v", j.Env)
		}
		if j.Name == "test" && got != "" {
			t.Errorf("normal step got the model: %v", j.Env)
		}
	}
}

func TestNoGuidanceMeansNoField(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if h.fake.inputs[0].Guidance != nil {
		t.Errorf("guidance = %+v, want absent", h.fake.inputs[0].Guidance)
	}
	for _, j := range h.fake.jobs {
		if _, ok := j.Env["PLUMBIX_AI_MODEL"]; ok {
			t.Errorf("%s got a model with none set", j.Name)
		}
	}
}

// TestCredentialsAreReadLive: a token that appears after the engine started
// is used by the next fetch, with no restart.
func TestCredentialsAreReadLive(t *testing.T) {
	const token = "late_token_42"
	values := map[string]string{}
	var mu sync.Mutex
	src := host.Live(func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range values {
			out[k] = v
		}
		return out
	})
	h := newHarness(t, Config{Hosts: src})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	repo := change.Repo{ID: "r_liverepo0001", Host: change.HostGitHub, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}
	repo.URL = h.repo.ServeHTTP("x-access-token", token)
	if err := h.db.CreateRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	h.repoID = repo.ID
	c := h.submit("feat")
	if r := h.validate(c.ID); r.Outcome != change.NeedsHuman {
		t.Fatalf("without a token the fetch must fail: %s", r.Outcome)
	}
	mu.Lock()
	values[host.GitHubTokenKey] = token
	mu.Unlock()
	if r := h.validate(c.ID); r.Outcome != change.Approved {
		t.Fatalf("with the token set later: %s (%s)", r.Outcome, r.Error)
	}
}
