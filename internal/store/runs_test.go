package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
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
		Findings: []validation.SourcedFinding{{Validator: "validate", Finding: validation.Finding{Severity: "major", Message: "m"}}}, Risks: []validation.SourcedRisk{}}
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
