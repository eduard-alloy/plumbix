package validation

import (
	"encoding/json"
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

// TestAdvisoryVerdictsAreRecordedButNeverCounted (WU-030, A31): an
// advisory validator's outcome, assessments, findings and risks appear in
// the result, tagged, and none of it moves the state or a criterion.
func TestAdvisoryVerdictsAreRecordedButNeverCounted(t *testing.T) {
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Approve, allMet)},
		{Name: "ai-grok", Advisory: true, Verdict: verdict(Reject, map[string]Status{"AC-1": Unmet}, Finding{Severity: "blocker", Message: "wrong"})},
	}, []string{"validate"})
	if r.State != change.Approved || len(r.Reasons) != 0 {
		t.Fatalf("an advisory rejection moved the state: %+v", r)
	}
	if len(r.Validators) != 2 || !r.Validators[1].Advisory || r.Validators[1].Outcome != Reject || r.Validators[0].Advisory {
		t.Errorf("validators = %+v", r.Validators)
	}
	ac1 := r.Criteria[0]
	if ac1.Status != Met || len(ac1.Assessments) != 2 || !ac1.Assessments[1].Advisory || ac1.Assessments[1].Status != Unmet {
		t.Errorf("AC-1 = %+v", ac1)
	}
	if len(r.Findings) != 1 || !r.Findings[0].Advisory || r.Findings[0].Validator != "ai-grok" {
		t.Errorf("findings = %+v", r.Findings)
	}

	// A human-required risk, a missing verdict, or being the only validator:
	// advisory never brings a human in.
	risky := verdict(Approve, nil)
	risky.Risks = []Risk{{Area: "security", Message: "look", HumanRequired: true}}
	r = combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Approve, allMet)},
		{Name: "ai-grok", Advisory: true, Verdict: risky},
		{Name: "ai-kimi", Advisory: true, Err: errors.New("validator step timed_out")},
	}, []string{"validate"})
	if r.State != change.Approved || len(r.Risks) != 1 || !r.Risks[0].Advisory || len(r.Validators) != 3 || r.Validators[2].Error == "" || !r.Validators[2].Advisory {
		t.Errorf("advisory risk or missing verdict counted: %+v", r)
	}
	r = combine([]ValidatorResult{{Name: "ai-grok", Advisory: true, Verdict: verdict(Approve, allMet)}}, nil)
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), "no validator ran") {
		t.Errorf("an advisory validator alone approved a change: %+v", r)
	}
	for _, c := range r.Criteria {
		if c.Status != Unknown {
			t.Errorf("%s judged by an advisory validator alone: %s", c.ID, c.Status)
		}
	}
	// A branch that marks the target's required validator advisory has not
	// satisfied it.
	r = combine([]ValidatorResult{
		{Name: "validate", Advisory: true, Verdict: verdict(Approve, allMet)},
		{Name: "other", Verdict: verdict(Approve, allMet)},
	}, []string{"validate"})
	if r.State != change.NeedsHuman || !strings.Contains(strings.Join(r.Reasons, " "), "required validator validate did not report") {
		t.Errorf("a required validator marked advisory counted as reported: %+v", r)
	}
}

// TestUsageIsCarried (WU-053): a validator's reported model usage is kept
// on its outcome as the verdict gave it, advisory or not, and is absent,
// not empty, when the validator reported none.
func TestUsageIsCarried(t *testing.T) {
	with := verdict(Approve, allMet)
	cost := 0.0412
	with.Usage = &Usage{Model: "claude-x", InputTokens: 1200, OutputTokens: 300, CacheReadTokens: 8000, CostUSD: &cost}
	adv := verdict(Reject, nil)
	adv.Usage = &Usage{Model: "grok-y", InputTokens: 1, OutputTokens: 2, Turns: 3}
	r := combine([]ValidatorResult{
		{Name: "validate", Verdict: verdict(Approve, allMet)},
		{Name: "ai-review", Verdict: with},
		{Name: "ai-grok", Advisory: true, Verdict: adv},
	}, []string{"validate", "ai-review"})
	if len(r.Validators) != 3 {
		t.Fatalf("validators = %+v", r.Validators)
	}
	if r.Validators[0].Usage != nil {
		t.Errorf("a validator that reported no usage has one: %+v", r.Validators[0].Usage)
	}
	if u := r.Validators[1].Usage; u == nil || u.CostUSD == nil || *u.CostUSD != cost || u.Model != with.Usage.Model {
		t.Errorf("ai-review usage = %+v, want %+v", u, with.Usage)
	}
	if u := r.Validators[2].Usage; u == nil || *u != *adv.Usage {
		t.Errorf("advisory usage = %+v, want %+v", u, adv.Usage)
	}
	data, err := json.Marshal(r.Validators)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw[0]["usage"]; ok {
		t.Errorf("usage key present without usage: %s", raw[0]["usage"])
	}
	want := `{"model":"claude-x","input_tokens":1200,"output_tokens":300,"cost_usd":0.0412,"cache_read_tokens":8000}`
	if got := string(raw[1]["usage"]); got != want {
		t.Errorf("usage json = %s, want %s", got, want)
	}
}
