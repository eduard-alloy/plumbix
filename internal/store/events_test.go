package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

const eventsSchemaPath = "../../contracts/events.schema.json"

func openDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func compileEventsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(eventsSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("events.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// schemaTypes returns the event types the schema's oneOf variants name.
func schemaTypes(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(eventsSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OneOf []struct {
			Properties struct {
				Type struct {
					Const string `json:"const"`
				} `json:"type"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, v := range doc.OneOf {
		if v.Properties.Type.Const == "" {
			t.Fatal("a oneOf variant has no type const")
		}
		types[v.Properties.Type.Const] = true
	}
	return types
}

func validateEvent(t *testing.T, schema *jsonschema.Schema, e events.Event) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(v); err != nil {
		t.Errorf("event %s does not match events.schema.json:\n%s\n%v", e.Type, data, err)
	}
}

// TestLifecycleEmitsEvents drives every emitting write once and checks the
// events: one per fact, increasing ids, notified in order, stored, and valid
// against the contract. It also keeps the schema's type list and
// events.Types the same set.
func TestLifecycleEmitsEvents(t *testing.T) {
	db := openDB(t)
	var notified []events.Event
	db.OnEvent(func(e events.Event) { notified = append(notified, e) })
	ctx := context.Background()
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	if err := db.CreateRepo(ctx, change.Repo{ID: "r_1", Host: "git", URL: "/tmp/r1.git", ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	c := change.Change{ID: "c_1", RepoID: "r_1", SourceBranch: "feat", TargetBranch: "main", Title: "T", Intent: "I", SubmittedBy: "kai",
		AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "works"}}, Constraints: []string{}, State: change.Open, CreatedAt: at, UpdatedAt: at}
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Branch: "feat", Iteration: 1, Status: run.Running, StartedAt: at.Add(time.Second)}
	if err := db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.Running, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunCommits(ctx, "run_1", "aaaa", "bbbb"); err != nil {
		t.Fatal(err)
	}
	step := events.Event{Type: events.StepFinished, RepoID: "r_1", ChangeID: "c_1", RunID: "run_1", Time: at.Add(2 * time.Second),
		Payload: events.StepFinishedPayload{Step: "test", Status: "succeeded", DurationMS: 10}}
	if _, err := db.AppendEvent(ctx, step); err != nil {
		t.Fatal(err)
	}
	result := &validation.Result{State: change.Approved, Criteria: []validation.CombinedCriterion{{ID: "AC-1", Text: "works", Status: validation.Met}}, Findings: []validation.SourcedFinding{}, Risks: []validation.SourcedRisk{}, Reasons: []string{}}
	if err := db.FinishRun(ctx, "run_1", run.Finish{Status: run.Finished, Outcome: change.Approved, Result: result, Steps: []run.Step{{Name: "test", Status: "succeeded", DurationMS: 10}}, At: at.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Running, change.Approved, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMerge(ctx, change.MergeRecord{ChangeID: "c_1", RepoID: "r_1", Branch: "main", Merge: change.Merge{Commit: "cccc", Previous: "dddd", Strategy: "squash", RunID: "run_1", MergedAt: at.Add(4 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Approved, change.Merged, at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	a, created, err := db.Raise(ctx, alert.Alert{Kind: alert.BypassCommit, Key: "main@eeee", RepoID: "r_1", Message: "bypass", CreatedAt: at.Add(5 * time.Second)})
	if err != nil || !created {
		t.Fatalf("raise: %v %v", created, err)
	}
	if _, created, err := db.Raise(ctx, alert.Alert{Kind: alert.BypassCommit, Key: "main@eeee", RepoID: "r_1", Message: "again", CreatedAt: at.Add(6 * time.Second)}); err != nil || created {
		t.Fatalf("raise again: created %v, err %v; a duplicate must emit nothing", created, err)
	}
	if err := db.Resolve(ctx, a.ID, at.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.Resolve(ctx, a.ID, at.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Raise(ctx, alert.Alert{Kind: alert.TokenExpiry, Key: "github", Message: "expires", CreatedAt: at.Add(9 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveKey(ctx, alert.TokenExpiry, "github", at.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveKey(ctx, alert.TokenExpiry, "github", at.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}

	want := []events.Type{
		events.ChangeSubmitted, events.RunStarted, events.StateChanged, events.StepFinished,
		events.RunFinished, events.VerdictIssued, events.StateChanged, events.ChangeMerged, events.StateChanged,
		events.AlertRaised, events.AlertResolved, events.AlertRaised, events.AlertResolved,
	}
	var got []events.Type
	for i, e := range notified {
		got = append(got, e.Type)
		if e.ID != int64(i+1) {
			t.Errorf("event %d has id %d; ids must be assigned in order", i, e.ID)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("notified types = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notified types = %v\nwant %v", got, want)
		}
	}

	stored, err := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(notified) {
		t.Fatalf("stored %d events, notified %d", len(stored), len(notified))
	}
	schema := compileEventsSchema(t)
	seen := map[string]bool{}
	for i, e := range stored {
		n := notified[i]
		if e.ID != n.ID || e.Type != n.Type || !e.Time.Equal(n.Time) || e.RepoID != n.RepoID || e.ChangeID != n.ChangeID || e.RunID != n.RunID {
			t.Errorf("stored event %d = %+v, notified %+v", i, e, n)
		}
		validateEvent(t, schema, e)
		validateEvent(t, schema, n)
		seen[string(e.Type)] = true
	}
	inSchema := schemaTypes(t)
	for _, ty := range events.Types {
		if !seen[string(ty)] {
			t.Errorf("type %s was never produced by this lifecycle", ty)
		}
		if !inSchema[string(ty)] {
			t.Errorf("type %s is missing from events.schema.json", ty)
		}
	}
	for ty := range inSchema {
		if !seen[ty] {
			t.Errorf("events.schema.json names type %s, which the code never emits", ty)
		}
	}

	// Payload details that matter to clients.
	var verdict events.VerdictIssuedPayload
	decodePayload(t, stored[5], &verdict)
	if verdict.Iteration != 1 || verdict.Outcome != "approved" || len(verdict.Criteria) != 1 || verdict.Criteria[0].Status != "met" {
		t.Errorf("verdict payload = %+v", verdict)
	}
	var finished events.RunFinishedPayload
	decodePayload(t, stored[4], &finished)
	if finished.Head != "aaaa" || finished.Base != "bbbb" || finished.Status != "finished" || finished.Outcome != "approved" || finished.Iteration != 1 || finished.Branch != "feat" {
		t.Errorf("run.finished payload = %+v", finished)
	}
	var merged events.ChangeMergedPayload
	decodePayload(t, stored[7], &merged)
	if merged.Commit != "cccc" || merged.Branch != "main" || stored[7].RunID != "run_1" {
		t.Errorf("change.merged = %+v (%+v)", merged, stored[7])
	}
	var raised events.AlertRaisedPayload
	decodePayload(t, stored[9], &raised)
	if raised.AlertID != a.ID || raised.Kind != "bypass_commit" || stored[9].RepoID != "r_1" {
		t.Errorf("alert.raised = %+v (%+v)", raised, stored[9])
	}
	var resolved events.AlertResolvedPayload
	decodePayload(t, stored[12], &resolved)
	if resolved.Kind != "token_expiry" || resolved.Key != "github" || resolved.AlertID == "" {
		t.Errorf("alert.resolved by key = %+v", resolved)
	}
}

func decodePayload(t *testing.T, e events.Event, v any) {
	t.Helper()
	data, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("payload of %s: %v", e.Type, err)
	}
}

func TestEventsAfterFiltersAndLimits(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for i, ch := range []string{"c_1", "c_2", "c_1", "c_1"} {
		repo := "r_1"
		if i == 1 {
			repo = "r_2"
		}
		if _, err := db.AppendEvent(ctx, events.Event{Type: events.StateChanged, RepoID: repo, ChangeID: ch, Payload: events.StateChangedPayload{From: "open", To: "running"}}); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(es []events.Event) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	cases := []struct {
		after int64
		f     events.Filter
		limit int
		want  []int64
	}{
		{0, events.Filter{}, 10, []int64{1, 2, 3, 4}},
		{2, events.Filter{}, 10, []int64{3, 4}},
		{0, events.Filter{}, 2, []int64{1, 2}},
		{0, events.Filter{ChangeID: "c_1"}, 10, []int64{1, 3, 4}},
		{0, events.Filter{RepoID: "r_2"}, 10, []int64{2}},
		{1, events.Filter{RepoID: "r_1", ChangeID: "c_1"}, 1, []int64{3}},
		{4, events.Filter{}, 10, nil},
	}
	for _, c := range cases {
		got, err := db.EventsAfter(ctx, c.after, c.f, c.limit)
		if err != nil {
			t.Fatal(err)
		}
		if g := ids(got); len(g) != len(c.want) || (len(g) > 0 && func() bool {
			for i := range g {
				if g[i] != c.want[i] {
					return true
				}
			}
			return false
		}()) {
			t.Errorf("EventsAfter(%d, %+v, %d) = %v, want %v", c.after, c.f, c.limit, g, c.want)
		}
	}
	e, err := db.AppendEvent(ctx, events.Event{Type: events.StateChanged, RepoID: "r_1", ChangeID: "c_9", Payload: events.StateChangedPayload{}})
	if err != nil || e.ID != 5 || e.Time.IsZero() {
		t.Errorf("AppendEvent = %+v, %v; want id 5 and a time", e, err)
	}
}

func TestFailedWritesEmitNothing(t *testing.T) {
	db := openDB(t)
	var notified []events.Event
	db.OnEvent(func(e events.Event) { notified = append(notified, e) })
	ctx := context.Background()
	now := time.Now()
	if err := db.CreateRepo(ctx, change.Repo{ID: "r_1", Host: "git", URL: "/tmp/r1.git", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c := change.Change{ID: "c_1", RepoID: "r_1", SourceBranch: "feat", TargetBranch: "main", Title: "T", Intent: "I", AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "x"}}, State: change.Open, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.ID = "c_2"
	if err := db.CreateChange(ctx, c); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("second open change on the branch: %v", err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Running, change.Approved, now); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("state change from the wrong state: %v", err)
	}
	if err := db.SetChangeState(ctx, "c_nope", change.Open, change.Running, now); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("state change of a missing change: %v", err)
	}
	if err := db.FinishRun(ctx, "run_nope", run.Finish{Status: run.Finished, At: now}); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("finish of a missing run: %v", err)
	}
	if err := db.Resolve(ctx, "al_nope", now); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("resolve of a missing alert: %v", err)
	}
	if len(notified) != 1 || notified[0].Type != events.ChangeSubmitted {
		t.Errorf("notified = %+v; only the successful submit should emit", notified)
	}
}

func TestStepFinishedPayloadCarriesRetriesAndReasons(t *testing.T) {
	schema := compileEventsSchema(t)
	for _, p := range []events.StepFinishedPayload{
		{Step: "flaky", Status: "failed", ExitCode: 1, DurationMS: 5, Attempts: 3, AllowedFailure: true},
		{Step: "api", Status: "skipped", Reason: "paths"},
		{Step: "after", Status: "skipped", Reason: "needs"},
		{Step: "build", Status: "succeeded", DurationMS: 10},
	} {
		validateEvent(t, schema, events.Event{ID: 1, Type: events.StepFinished, RepoID: "r_1", RunID: "run_1", Time: t0, Payload: p})
	}
	data, _ := json.Marshal(events.StepFinishedPayload{Step: "build", Status: "succeeded"})
	if s := string(data); strings.Contains(s, "attempts") || strings.Contains(s, "reason") || strings.Contains(s, "allowed") {
		t.Errorf("plain step payload carries empty optional fields: %s", s)
	}
}
