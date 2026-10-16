package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

var crit3 = []change.Criterion{{ID: "AC-1", Text: "a"}, {ID: "AC-2", Text: "b"}, {ID: "AC-3", Text: "c"}}

func verdict(outcome Outcome, statuses map[string]Status, findings ...Finding) *Verdict {
	v := &Verdict{Plumbix: VerdictVersion, Outcome: outcome, Summary: "s", Findings: findings}
	for _, id := range []string{"AC-1", "AC-2", "AC-3"} {
		if st, ok := statuses[id]; ok {
			v.Criteria = append(v.Criteria, CriterionVerdict{ID: id, Status: st, Reason: "r"})
		}
	}
	return v
}

var allMet = map[string]Status{"AC-1": Met, "AC-2": Met, "AC-3": Met}

func combine(results []ValidatorResult, required []string, changed ...string) Result {
	return Combine(crit3, results, required, changed)
}

func TestAllApproveAllMet(t *testing.T) {
	r := combine([]ValidatorResult{{Name: "validate", Verdict: verdict(Approve, allMet)}}, []string{"validate"})
	if r.State != change.Approved || len(r.Reasons) != 0 {
		t.Fatalf("result = %+v", r)
	}
	for _, c := range r.Criteria {
		if c.Status != Met {
			t.Errorf("%s = %s", c.ID, c.Status)
		}
	}
}

func TestRejectWins(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "a", Verdict: verdict(Fix, map[string]Status{"AC-1": Unmet})},
		{Name: "b", Verdict: verdict(Reject, nil, Finding{Severity: "blocker", Message: "wrong"})},
	}, []string{"a", "b"})
	if r.State != change.Rejected {
		t.Fatalf("state = %s", r.State)
	}
}

func TestFixCollectsTaggedFindings(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Fix, map[string]Status{"AC-1": Unmet, "AC-2": Met, "AC-3": Met}, Finding{Severity: "major", Message: "add test"})},
		{Name: "ai-review", Verdict: verdict(Approve, allMet)},
	}, []string{"validate", "ai-review"})
	if r.State != change.FixRequested {
		t.Fatalf("state = %s", r.State)
	}
	if len(r.Findings) != 1 || r.Findings[0].Validator != "validate" || r.Findings[0].Message != "add test" {
		t.Errorf("findings = %+v", r.Findings)
	}
	if r.Criteria[0].Status != Unmet || len(r.Criteria[0].Assessments) != 2 {
		t.Errorf("AC-1 = %+v; unmet must win and both assessments be kept", r.Criteria[0])
	}
}

func TestAIMetCoversUncheckedCriterion(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Approve, map[string]Status{"AC-1": Met, "AC-2": Met, "AC-3": Unknown})},
		{Name: "ai-review", Verdict: verdict(Approve, allMet)},
	}, []string{"validate", "ai-review"})
	if r.State != change.Approved {
		t.Fatalf("state = %s, reasons %v", r.State, r.Reasons)
	}
}

func TestUnknownCriterionNeedsHuman(t *testing.T) {
	r := combine([]ValidatorResult{{Name: "validate", Verdict: verdict(Approve, map[string]Status{"AC-1": Met, "AC-2": Met})}}, []string{"validate"})
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), "AC-3") {
		t.Fatalf("state = %s, reasons %v", r.State, r.Reasons)
	}
}

func TestValidatorErrorNeedsHuman(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Approve, allMet)},
		{Name: "ai-review", Err: errors.New("validator wrote no verdict")},
	}, []string{"validate", "ai-review"})
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), "ai-review") {
		t.Fatalf("state = %s, reasons %v", r.State, r.Reasons)
	}
}

func TestFixBeatsValidatorError(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Fix, map[string]Status{"AC-1": Unmet})},
		{Name: "ai-review", Err: errors.New("crashed")},
	}, []string{"validate", "ai-review"})
	if r.State != change.FixRequested {
		t.Fatalf("state = %s", r.State)
	}
}

func TestMissingRequiredValidatorNeedsHuman(t *testing.T) {
	r := combine([]ValidatorResult{{Name: "validate", Verdict: verdict(Approve, allMet)}}, []string{"validate", "ai-review"})
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), "ai-review") {
		t.Fatalf("state = %s, reasons %v", r.State, r.Reasons)
	}
}

func TestNoValidatorsNeverApproves(t *testing.T) {
	r := combine(nil, nil)
	if r.State != change.NeedsHuman {
		t.Fatalf("state = %s; a change with no validators must not be approved", r.State)
	}
}

func TestPipelineChangeNeedsHuman(t *testing.T) {
	r := combine([]ValidatorResult{{Name: "validate", Verdict: verdict(Approve, allMet)}}, []string{"validate"}, "main.go", ".plumbix/pipeline.yaml")
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), ".plumbix/") {
		t.Fatalf("state = %s, reasons %v", r.State, r.Reasons)
	}
}

func TestHumanRequiredRiskNeedsHuman(t *testing.T) {
	v := verdict(Approve, allMet)
	v.Risks = []Risk{{Area: "auth", Message: "touches login", HumanRequired: true}}
	r := combine([]ValidatorResult{{Name: "validate", Verdict: v}}, []string{"validate"})
	if r.State != change.NeedsHuman || len(r.Risks) != 1 || r.Risks[0].Validator != "validate" {
		t.Fatalf("result = %+v", r)
	}
}

// TestValidatorsAreRecorded: the combined result keeps each validator's own
// decision, so a client can show what a validator said, not only whether
// its step ran.
func TestValidatorsAreRecorded(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Fix, map[string]Status{"AC-1": Unmet, "AC-2": Met, "AC-3": Met})},
		{Name: "ai-review", Verdict: verdict(Approve, allMet)},
		{Name: "broken", Err: errors.New("validator step errored")},
	}, []string{"validate", "ai-review", "broken"})
	if len(r.Validators) != 3 {
		t.Fatalf("validators = %+v", r.Validators)
	}
	want := []ValidatorOutcome{
		{Name: "validate", Outcome: Fix, Summary: "s"},
		{Name: "ai-review", Outcome: Approve, Summary: "s"},
		{Name: "broken", Error: "validator step errored"},
	}
	if silent := combine([]ValidatorResult{{Name: "silent"}}, []string{"silent"}); silent.Validators[0].Error != "no verdict file" {
		t.Errorf("validator with neither verdict nor error = %+v", silent.Validators[0])
	}
	for i, w := range want {
		if r.Validators[i] != w {
			t.Errorf("validator %d = %+v, want %+v", i, r.Validators[i], w)
		}
	}
	if none := combine(nil, nil); none.Validators == nil {
		t.Error("validators must be an empty list, never null")
	}
}
