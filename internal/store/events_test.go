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
	"github.com/eduard-alloy/plumbix/internal/review"
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
	// A second change stops at needs_human and a person approves it.
	c2 := c
	c2.ID, c2.SourceBranch, c2.CreatedAt, c2.UpdatedAt = "c_2", "pipe", at.Add(12*time.Second), at.Add(12*time.Second)
	if err := db.CreateChange(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeState(ctx, "c_2", change.Open, change.NeedsHuman, at.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.ApproveByPerson(ctx, "c_2", change.NeedsHuman, change.HumanApproval{Reason: "read it myself", Head: "ffff", At: at.Add(14 * time.Second), By: "tester", Source: change.ApprovalSourcePlumbix}); err != nil {
		t.Fatal(err)
	}
	// Plumbix brings main into its own branch; the engine appends the event itself.
	if _, err := db.AppendEvent(ctx, events.Event{Type: events.ChangeRefreshed, Time: at.Add(15 * time.Second), RepoID: "r_1", ChangeID: "c_2",
		Payload: events.ChangeRefreshedPayload{FromHead: "ffff", Head: "abab", TargetHead: "cdcd"}}); err != nil {
		t.Fatal(err)
	}
	// A step begins: the engine appends step.started itself, like step.finished.
	if _, err := db.AppendEvent(ctx, events.Event{Type: events.StepStarted, Time: at.Add(15500 * time.Millisecond), RepoID: "r_1", ChangeID: "c_2", RunID: "run_1",
		Payload: events.StepStartedPayload{Step: "build", Validator: false}}); err != nil {
		t.Fatal(err)
	}
	// An external validator step of c_2's run publishes a review and a reviewer takes it.
	if err := db.CreateRun(ctx, run.Run{ID: "run_2", RepoID: "r_1", ChangeID: "c_2", Iteration: 1, Status: run.Running, StartedAt: at.Add(16 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateReview(ctx, review.Review{ID: "rv_1", RunID: "run_2", RepoID: "r_1", ChangeID: "c_2", Step: "ai-review", Brief: json.RawMessage(`{}`),
		CreatedAt: at.Add(17 * time.Second), ExpiresAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.ClaimReview(ctx, "", "reviewer", "h", at.Add(18*time.Second)); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := db.ReleaseReview(ctx, "rv_1"); err != nil {
		t.Fatal(err)
	}

	want := []events.Type{
		events.ChangeSubmitted, events.RunStarted, events.StateChanged, events.StepFinished,
		events.RunFinished, events.VerdictIssued, events.StateChanged, events.ChangeMerged, events.StateChanged,
		events.AlertRaised, events.AlertResolved, events.AlertRaised, events.AlertResolved,
		events.ChangeSubmitted, events.StateChanged, events.ApprovedByPerson, events.StateChanged, events.ChangeRefreshed,
		events.StepStarted, events.RunStarted, events.ReviewRequested, events.ReviewClaimed, events.ReviewReleased,
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

	// A retired repository emits repo.removed (WU-065): one with no change left.
	if err := db.CreateRepo(ctx, sampleRepo("r_9", "https://github.com/a/retired")); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRepo(ctx, "r_9", "eduard@mac", at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
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

func TestApproveByPersonRecordsReasonAndEmitsBothEvents(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.NeedsHuman, t0); err != nil {
		t.Fatal(err)
	}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	at := t0.Add(time.Minute)
	if err := db.ApproveByPerson(ctx, "c_1", change.NeedsHuman, change.HumanApproval{Reason: "pipeline edit reviewed by hand", Head: "abc123", At: at, By: "tester", Source: change.ApprovalSourcePlumbix}); err != nil {
		t.Fatal(err)
	}
	c, _ := db.GetChange(ctx, "c_1")
	if c.State != change.Approved || c.HumanApproval == nil || c.HumanApproval.Reason != "pipeline edit reviewed by hand" || c.HumanApproval.Head != "abc123" || !c.HumanApproval.At.Equal(at) {
		t.Fatalf("change = %+v %+v", c.State, c.HumanApproval)
	}
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	if len(fresh) != 2 || fresh[0].Type != events.ApprovedByPerson || fresh[1].Type != events.StateChanged {
		t.Fatalf("events = %+v", fresh)
	}
	for _, e := range fresh {
		validateEvent(t, schema, e)
	}
	raw, _ := json.Marshal(fresh[0].Payload)
	var p events.ApprovedByPersonPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.Reason != "pipeline edit reviewed by hand" || p.Head != "abc123" || p.By != "tester" || p.Source != change.ApprovalSourcePlumbix {
		t.Errorf("payload = %s %v", raw, err)
	}
	if c, _ = db.GetChange(ctx, "c_1"); c.HumanApproval == nil || c.HumanApproval.By != "tester" || c.HumanApproval.Source != change.ApprovalSourcePlumbix {
		t.Errorf("the change's approval names %+v", c.HumanApproval)
	}
	// Approving again is a conflict: the change is no longer needs_human.
	if err := db.ApproveByPerson(ctx, "c_1", change.NeedsHuman, change.HumanApproval{Reason: "again", Head: "abc123", At: at, By: "tester", Source: change.ApprovalSourcePlumbix}); !errors.Is(err, change.ErrConflict) {
		t.Errorf("second approval err = %v", err)
	}
	// A new run ends the approval.
	if err := db.SetChangeState(ctx, "c_1", change.Approved, change.Running, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c, _ = db.GetChange(ctx, "c_1"); c.HumanApproval != nil {
		t.Errorf("approval survived a new run: %+v", c.HumanApproval)
	}
	// The person and the source are cleared with the rest, not only hidden behind a nil approval.
	var by, source string
	if err := db.sql.QueryRowContext(ctx, `SELECT approved_by, approved_source FROM changes WHERE id = 'c_1'`).Scan(&by, &source); err != nil || by != "" || source != "" {
		t.Errorf("after a new run approved_by=%q approved_source=%q err=%v", by, source, err)
	}
	if err := db.ApproveByPerson(ctx, "c_nope", change.NeedsHuman, change.HumanApproval{Reason: "x", Head: "h", At: at, By: "tester", Source: change.ApprovalSourcePlumbix}); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown change err = %v", err)
	}
}

func TestPlumbixBranchRoundTripsAndARefreshIsAnEvent(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	own := sampleChange("c_1", "r_1", "pipeline/x")
	own.PlumbixBranch = true
	if err := db.CreateChange(ctx, own); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_2", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.GetChange(ctx, "c_1"); !c.PlumbixBranch {
		t.Error("plumbix_branch was not stored")
	}
	if c, _ := db.GetChange(ctx, "c_2"); c.PlumbixBranch {
		t.Error("plumbix_branch leaked onto another change")
	}
	list, _ := db.ListChanges(ctx, change.Filter{RepoID: "r_1"})
	if len(list) != 2 || !list[0].PlumbixBranch || list[1].PlumbixBranch {
		t.Errorf("list = %+v", list)
	}
	e, err := db.AppendEvent(ctx, events.Event{Type: events.ChangeRefreshed, Time: t0, RepoID: "r_1", ChangeID: "c_1",
		Payload: events.ChangeRefreshedPayload{FromHead: "aaa111", Head: "bbb222", TargetHead: "ccc333"}})
	if err != nil {
		t.Fatal(err)
	}
	validateEvent(t, schema, e)
	raw, _ := json.Marshal(e.Payload)
	var p events.ChangeRefreshedPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.FromHead != "aaa111" || p.Head != "bbb222" || p.TargetHead != "ccc333" {
		t.Errorf("payload = %s %v", raw, err)
	}
}

// TestMergeEventCarriesAutoMerge: a GitHub merge made on the requester's
// behalf is recorded with auto_merge on the change and in change.merged,
// and the payload validates against the events schema (WU-063).
func TestMergeEventCarriesAutoMerge(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err := db.RecordMerge(ctx, change.MergeRecord{ChangeID: "c_1", RepoID: "r_1", Branch: "main",
		Merge: change.Merge{Commit: "cccc", Previous: "dddd", Strategy: change.StrategyGitHub, MergedBy: "eduard", AutoMerge: true, MergedAt: t0}}); err != nil {
		t.Fatal(err)
	}
	c, _ := db.GetChange(ctx, "c_1")
	if c.Merge == nil || !c.Merge.AutoMerge || c.Merge.MergedBy != "eduard" {
		t.Fatalf("merge = %+v", c.Merge)
	}
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	if len(fresh) != 1 || fresh[0].Type != events.ChangeMerged {
		t.Fatalf("events = %+v", fresh)
	}
	validateEvent(t, schema, fresh[0])
	var p events.ChangeMergedPayload
	decodePayload(t, fresh[0], &p)
	if !p.AutoMerge || p.MergedBy != "eduard" || p.Strategy != change.StrategyGitHub {
		t.Errorf("payload = %+v", p)
	}
}

// TestSubmittedEventCarriesTheCriteriaSet: a change opened from a criteria
// set names it in change.submitted, and the payload validates against the
// events schema; the set is frozen by the write, inside its transaction,
// and only at the version the criteria were read at (WU-066).
func TestSubmittedEventCarriesTheCriteriaSet(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	cs := change.CriteriaSet{ID: "cs_abc123def456", Task: "kai:plumbix/X6Q5JY", Owner: "eduard@mac", Criteria: []change.Criterion{{ID: "AC-1", Text: "works"}}, CreatedAt: t0, UpdatedAt: t0}
	if err := db.CreateCriteriaSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	stale := sampleChange("c_0", "r_1", "stale")
	stale.CriteriaSet = &change.CriteriaSetRef{ID: cs.ID, Owner: cs.Owner, Task: cs.Task, Version: t0.Add(-time.Minute)}
	if err := db.CreateChange(ctx, stale); !errors.Is(err, change.ErrCriteriaSetChanged) {
		t.Fatalf("a reference at another version: %v", err)
	}
	if got, _ := db.GetCriteriaSet(ctx, cs.ID); got.Frozen {
		t.Fatal("a refused write froze the set")
	}
	c := sampleChange("c_1", "r_1", "feat")
	c.CriteriaSet = &change.CriteriaSetRef{ID: cs.ID, Owner: cs.Owner, Task: cs.Task, Version: t0}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	if len(fresh) != 1 || fresh[0].Type != events.ChangeSubmitted {
		t.Fatalf("events = %+v", fresh)
	}
	validateEvent(t, schema, fresh[0])
	var p events.ChangeSubmittedPayload
	decodePayload(t, fresh[0], &p)
	if p.CriteriaSet == nil || p.CriteriaSet.ID != cs.ID || p.CriteriaSet.Owner != "eduard@mac" || p.CriteriaSet.Task != cs.Task {
		t.Errorf("payload = %+v", p)
	}
	got, _ := db.GetCriteriaSet(ctx, cs.ID)
	if !got.Frozen || len(got.UsedBy) != 1 || got.UsedBy[0] != "c_1" {
		t.Errorf("set after use = frozen %v used_by %v", got.Frozen, got.UsedBy)
	}
	// A pull request edit that drops the reference leaves the set frozen.
	if err := db.UpdateChangeText(ctx, "c_1", "t", "i", []change.Criterion{{ID: "AC-1", Text: "own"}}, nil, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetCriteriaSet(ctx, cs.ID)
	if !got.Frozen || len(got.UsedBy) != 0 {
		t.Errorf("set after the reference was dropped = frozen %v used_by %v", got.Frozen, got.UsedBy)
	}
	cs.Criteria = []change.Criterion{{ID: "AC-1", Text: "changed"}}
	if err := db.UpdateCriteriaSet(ctx, cs); !errors.Is(err, change.ErrCriteriaSetUsed) {
		t.Errorf("update of a frozen set: %v", err)
	}
	// A reference with a problem names a set the change did not use and freezes nothing.
	if err := db.CreateCriteriaSet(ctx, change.CriteriaSet{ID: "cs_free00000000", Task: "t", Owner: "o", Criteria: []change.Criterion{{ID: "AC-1", Text: "x"}}, CreatedAt: t0, UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	c2 := sampleChange("c_2", "r_1", "feat2")
	c2.CriteriaSet = &change.CriteriaSetRef{ID: "cs_free00000000", Problem: "no criteria set cs_free00000000 on this server"}
	if err := db.CreateChange(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetCriteriaSet(ctx, "cs_free00000000"); got.Frozen {
		t.Error("a set named with a problem was frozen")
	}
}

// TestRepoRemovedEvent: retiring a repository emits repo.removed naming
// who asked, valid against the schema, and is refused inside the same
// transaction while a change of it is open (WU-065).
func TestRepoRemovedEvent(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRepo(ctx, "r_1", "eduard@mac", t0); !errors.Is(err, change.ErrRepoBusy) {
		t.Fatalf("remove with an open change: %v", err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.Closed, t0); err != nil {
		t.Fatal(err)
	}
	// A running run of the repository, a branch run with no change, holds the mirror.
	if err := db.CreateRun(ctx, run.Run{ID: "run_b", RepoID: "r_1", Branch: "main", Iteration: 1, Status: run.Running, StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRepo(ctx, "r_1", "eduard@mac", t0); !errors.Is(err, change.ErrRepoBusy) {
		t.Fatalf("remove with a running branch run: %v", err)
	}
	if err := db.FinishRun(ctx, "run_b", run.Finish{Status: run.Finished, Steps: []run.Step{}, At: t0.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err := db.RemoveRepo(ctx, "r_1", "eduard@mac", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A change written after the removal is refused in the write itself.
	if err := db.CreateChange(ctx, sampleChange("c_late", "r_1", "late")); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("a change on a removed repository: %v", err)
	}
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	if len(fresh) != 1 || fresh[0].Type != events.RepoRemoved || fresh[0].RepoID != "r_1" {
		t.Fatalf("events = %+v", fresh)
	}
	validateEvent(t, schema, fresh[0])
	var p events.RepoRemovedPayload
	decodePayload(t, fresh[0], &p)
	if p.By != "eduard@mac" || p.URL != "https://github.com/a/b" || p.Host != "github" {
		t.Errorf("payload = %+v", p)
	}
	if _, err := db.GetRepo(ctx, "r_1"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("removed repository found: %v", err)
	}
	if err := db.RemoveRepo(ctx, "r_1", "again", t0); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
}

// TestPausedRepoIsListedAsPaused: the paused flag round-trips through the
// single lookup and the listing (WU-065).
func TestPausedRepoIsListedAsPaused(t *testing.T) {
	db := openDB(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRepoPaused(ctx, "r_1", true); err != nil {
		t.Fatal(err)
	}
	one, _ := db.GetRepo(ctx, "r_1")
	all, _ := db.ListRepos(ctx)
	if !one.Paused || len(all) != 1 || !all[0].Paused {
		t.Errorf("one = %+v, all = %+v", one.Paused, all)
	}
}
