package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
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
