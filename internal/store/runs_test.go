package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

func withChange(t *testing.T) *DB {
	t.Helper()
	db, _ := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRunLifecycle(t *testing.T) {
	db := withChange(t)
	r := run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Iteration: 1, Status: run.Running, StartedAt: t0}
	if err := db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunCommits(ctx, "run_1", "head1", "base1"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRun(ctx, "run_1")
	if err != nil || got.Head != "head1" || got.Base != "base1" || got.Status != run.Running || got.FinishedAt != nil || len(got.Steps) != 0 {
		t.Fatalf("running run = %+v, %v", got, err)
	}
	running, _ := db.RunningRuns(ctx)
	if len(running) != 1 {
		t.Errorf("running = %v", running)
	}

	result := &validation.Result{State: change.FixRequested, Reasons: []string{}, Criteria: []validation.CombinedCriterion{{ID: "AC-1", Text: "a", Status: validation.Unmet}},
		Findings: []validation.SourcedFinding{{Validator: "validate", Finding: validation.Finding{Severity: "major", Message: "m"}}}, Risks: []validation.SourcedRisk{},
		Validators: []validation.ValidatorOutcome{{Name: "validate", Outcome: validation.Fix, Summary: "s"}}}
	steps := []run.Step{{Name: "test", Status: "failed", ExitCode: 1, DurationMS: 1200}, {Name: "validate", Validator: true, Status: "succeeded", DurationMS: 300}}
	end := t0.Add(time.Minute)
	if err := db.FinishRun(ctx, "run_1", run.Finish{Status: run.Finished, Outcome: change.FixRequested, Result: result, Steps: steps, At: end}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRun(ctx, "run_1")
	if got.Status != run.Finished || got.Outcome != change.FixRequested || got.FinishedAt == nil || !got.FinishedAt.Equal(end) {
		t.Errorf("finished run = %+v", got)
	}
	if !reflect.DeepEqual(got.Steps, steps) || !reflect.DeepEqual(got.Result, result) {
		t.Errorf("steps %+v result %+v", got.Steps, got.Result)
	}
	if running, _ := db.RunningRuns(ctx); len(running) != 0 {
		t.Errorf("still running: %v", running)
	}
}

func TestIterationsAndList(t *testing.T) {
	db := withChange(t)
	if n, err := db.LastIteration(ctx, "c_1"); err != nil || n != 0 {
		t.Fatalf("last iteration = %d, %v", n, err)
	}
	for i := 1; i <= 2; i++ {
		if err := db.CreateRun(ctx, run.Run{ID: "run_" + string(rune('0'+i)), RepoID: "r_1", ChangeID: "c_1", Iteration: i, Status: run.Running, StartedAt: t0.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := db.LastIteration(ctx, "c_1"); n != 2 {
		t.Errorf("last iteration = %d, want 2", n)
	}
	if err := db.CreateRun(ctx, run.Run{ID: "run_dup", RepoID: "r_1", ChangeID: "c_1", Iteration: 2, Status: run.Running, StartedAt: t0}); !errors.Is(err, change.ErrConflict) {
		t.Errorf("duplicate iteration: err = %v", err)
	}
	list, err := db.ListRuns(ctx, "c_1")
	if err != nil || len(list) != 2 || list[0].Iteration != 1 || list[1].Iteration != 2 {
		t.Errorf("list = %+v, %v", list, err)
	}
	if _, err := db.GetRun(ctx, "run_none"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("missing run: %v", err)
	}
}

// TestOldResultsGetAnEmptyValidatorsList: results stored before the field
// existed come back with an empty list, so the contract holds for them.
func TestOldResultsGetAnEmptyValidatorsList(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.CreateRepo(ctx, change.Repo{ID: "r_1", Host: "git", URL: "/tmp/r.git", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, change.Change{ID: "c_1", RepoID: "r_1", SourceBranch: "f", TargetBranch: "main", Title: "t", Intent: "i", AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "x"}}, State: change.Open, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Branch: "f", Iteration: 1, Status: run.Running, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, "run_1", run.Finish{Status: run.Finished, Outcome: change.Approved, Result: &validation.Result{State: change.Approved}, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE runs SET result = ? WHERE id = ?`, `{"state":"approved","criteria":[],"findings":[],"risks":[],"reasons":[]}`, "run_1"); err != nil {
		t.Fatal(err)
	}
	r, err := db.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Result == nil || r.Result.Validators == nil || len(r.Result.Validators) != 0 {
		t.Fatalf("validators = %#v, want an empty list", r.Result)
	}
}

func TestStepDetailsRoundTrip(t *testing.T) {
	db := withChange(t)
	if err := db.CreateRun(ctx, run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Iteration: 1, Status: run.Running, StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	flaky := run.Step{Name: "flaky", Status: "failed", ExitCode: 1, DurationMS: 50, Attempts: 3, AllowedFailure: true, Stage: "checks"}
	api := run.Step{Name: "api", Status: "skipped", Reason: "paths", Stage: "checks"}
	for i, s := range []run.Step{flaky, api} {
		if err := db.RecordStep(ctx, "run_1", i, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []run.Step{flaky, api}; !reflect.DeepEqual(got.Steps, want) {
		t.Errorf("recorded steps:\n got %+v\nwant %+v", got.Steps, want)
	}
	plain := run.Step{Name: "build", Status: "succeeded", DurationMS: 10}
	if err := db.FinishRun(ctx, "run_1", run.Finish{Status: run.Finished, Outcome: change.Approved, Result: &validation.Result{State: change.Approved}, Steps: []run.Step{plain, flaky, api}, At: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRun(ctx, "run_1")
	if want := []run.Step{plain, flaky, api}; !reflect.DeepEqual(got.Steps, want) {
		t.Errorf("finished steps:\n got %+v\nwant %+v", got.Steps, want)
	}
}

// TestRunRecordsItsPipeline (WU-045): a triggered pipeline's run keeps the
// pipeline's name, and run.started carries it.
func TestRunRecordsItsPipeline(t *testing.T) {
	db := withChange(t)
	r := run.Run{ID: "run_p", RepoID: "r_1", Branch: "main", Pipeline: "deploy", Status: run.Running, StartedAt: t0}
	if err := db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRun(ctx, "run_p")
	if err != nil || got.Pipeline != "deploy" {
		t.Fatalf("run = %+v, %v", got, err)
	}
	evs, err := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	var p events.RunStartedPayload
	if raw, ok := last.Payload.(json.RawMessage); !ok || json.Unmarshal(raw, &p) != nil || p.Pipeline != "deploy" {
		t.Errorf("run.started payload = %s", last.Payload)
	}
}
