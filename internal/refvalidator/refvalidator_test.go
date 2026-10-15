package refvalidator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

type fixture struct {
	in, workspace string
}

func setup(t *testing.T, criteria []change.Criterion, changed []string, evidence []validation.StepEvidence) fixture {
	t.Helper()
	f := fixture{in: filepath.Join(t.TempDir(), "in"), workspace: t.TempDir()}
	if err := os.WriteFile(filepath.Join(f.workspace, "README.md"), []byte("Uploads now retry three times.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := change.Change{ID: "c_aaaaaaaaaaaa", Title: "t", Intent: "i", SourceBranch: "feat", TargetBranch: "main", AcceptanceCriteria: criteria}
	if err := validation.PrepareInputs(f.in, validation.NewInput(c, "b", "h", changed, 1, nil), evidence); err != nil {
		t.Fatal(err)
	}
	return f
}

func steps(names ...string) *change.Check { return &change.Check{Steps: names} }
func command(cmd string) *change.Check    { return &change.Check{Command: cmd} }
func ok(step string) validation.StepEvidence {
	return validation.StepEvidence{Step: step, Status: "succeeded"}
}

func run(t *testing.T, f fixture, timeout time.Duration) validation.Verdict {
	t.Helper()
	v, err := Run(context.Background(), f.in, f.workspace, Options{CommandTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func statusByID(v validation.Verdict) map[string]validation.CriterionVerdict {
	m := map[string]validation.CriterionVerdict{}
	for _, c := range v.Criteria {
		m[c.ID] = c
	}
	return m
}

func TestCriteriaAssessment(t *testing.T) {
	crit := []change.Criterion{
		{ID: "AC-1", Text: "tests pass", Check: steps("test")},
		{ID: "AC-2", Text: "tests and lint pass", Check: steps("test", "lint")},
		{ID: "AC-3", Text: "e2e passes", Check: steps("e2e")},
		{ID: "AC-4", Text: "docs mention retries", Check: command("grep -q retry README.md")},
		{ID: "AC-5", Text: "changelog exists", Check: command("test -f CHANGELOG.md")},
		{ID: "AC-6", Text: "feels fast"},
	}
	f := setup(t, crit, nil, []validation.StepEvidence{ok("test"), {Step: "lint", Status: "failed", ExitCode: 1}})
	v := run(t, f, 10*time.Second)
	got := statusByID(v)
	want := map[string]validation.Status{"AC-1": validation.Met, "AC-2": validation.Unmet, "AC-3": validation.Unknown, "AC-4": validation.Met, "AC-5": validation.Unmet, "AC-6": validation.Unknown}
	for id, st := range want {
		if got[id].Status != st {
			t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Reason, st)
		}
		if strings.TrimSpace(got[id].Reason) == "" {
			t.Errorf("%s has no reason", id)
		}
	}
	if !strings.Contains(got["AC-2"].Reason, "lint") {
		t.Errorf("AC-2 reason %q should name the failed step", got["AC-2"].Reason)
	}
	if len(got["AC-1"].Evidence) == 0 || got["AC-1"].Evidence[0] != "evidence/test/log.txt" {
		t.Errorf("AC-1 evidence = %v", got["AC-1"].Evidence)
	}
	if v.Outcome != validation.Fix || len(v.Findings) != 2 {
		t.Errorf("outcome %s with %d findings, want fix with one finding per unmet criterion", v.Outcome, len(v.Findings))
	}
	data, _ := json.Marshal(v)
	if _, err := validation.ParseVerdict(data, crit); err != nil {
		t.Errorf("verdict breaks the contract: %v", err)
	}
}

func TestApproveWhenNothingUnmet(t *testing.T) {
	crit := []change.Criterion{{ID: "AC-1", Text: "tests", Check: steps("test")}, {ID: "AC-2", Text: "vibes"}}
	v := run(t, setup(t, crit, nil, []validation.StepEvidence{ok("test")}), time.Second)
	if v.Outcome != validation.Approve || len(v.Findings) != 0 {
		t.Fatalf("outcome %s, findings %v", v.Outcome, v.Findings)
	}
}

func TestCommandTimeout(t *testing.T) {
	crit := []change.Criterion{{ID: "AC-1", Text: "slow", Check: command("sleep 5")}}
	start := time.Now()
	v := run(t, setup(t, crit, nil, nil), 300*time.Millisecond)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %s; the time limit was not enforced", time.Since(start))
	}
	c := statusByID(v)["AC-1"]
	if c.Status != validation.Unmet || !strings.Contains(c.Reason, "time limit") {
		t.Errorf("AC-1 = %+v", c)
	}
}

func TestCommandRunsInWorkspace(t *testing.T) {
	crit := []change.Criterion{{ID: "AC-1", Text: "readme here", Check: command("test -f README.md && pwd")}}
	f := setup(t, crit, nil, nil)
	if c := statusByID(run(t, f, 5*time.Second))["AC-1"]; c.Status != validation.Met {
		t.Errorf("AC-1 = %+v", c)
	}
}

func TestStepStatusesThatAreNotSuccess(t *testing.T) {
	for _, st := range []string{"failed", "timed_out", "errored", "skipped", "cancelled"} {
		crit := []change.Criterion{{ID: "AC-1", Text: "x", Check: steps("test")}}
		v := run(t, setup(t, crit, nil, []validation.StepEvidence{{Step: "test", Status: st}}), time.Second)
		if c := statusByID(v)["AC-1"]; c.Status != validation.Unmet {
			t.Errorf("step %s: AC-1 = %s, want unmet", st, c.Status)
		}
	}
}

func TestRisks(t *testing.T) {
	crit := []change.Criterion{{ID: "AC-1", Text: "x"}}
	changed := []string{"upload.go", "upload_test.go", "web/src/app.spec.ts", "tests/test_upload.py", ".plumbix/pipeline.yaml"}
	v := run(t, setup(t, crit, changed, nil), time.Second)
	var tests, pipeline *validation.Risk
	for i := range v.Risks {
		switch v.Risks[i].Area {
		case "tests":
			tests = &v.Risks[i]
		case "pipeline":
			pipeline = &v.Risks[i]
		}
	}
	if tests == nil || !strings.Contains(tests.Message, "3 test files") || tests.HumanRequired {
		t.Errorf("tests risk = %+v", tests)
	}
	if pipeline == nil || !pipeline.HumanRequired {
		t.Errorf("pipeline risk = %+v", pipeline)
	}
}

func TestMissingInputIsError(t *testing.T) {
	if _, err := Run(context.Background(), t.TempDir(), t.TempDir(), Options{CommandTimeout: time.Second}); err == nil {
		t.Fatal("expected an error when change.json is missing")
	}
}
