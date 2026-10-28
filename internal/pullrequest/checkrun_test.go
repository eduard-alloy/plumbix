package pullrequest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

func verdictSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open("../../contracts/verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("verdict.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func three() *int { n := 3; return &n }

// finishedRun is a run of two validators: a counting one that asks for a
// fix with a located finding and reports usage, and an advisory one.
func finishedRun() run.Run {
	res := &validation.Result{State: change.FixRequested, Reasons: []string{},
		Criteria: []validation.CombinedCriterion{{ID: "AC-1", Text: "Tests pass", Status: validation.Unmet, Assessments: []validation.Assessment{
			{Validator: "ref", Status: validation.Unmet, Reason: "step test failed", Evidence: []string{"evidence/test/log.txt"}},
			{Validator: "second", Status: validation.Met, Reason: "looks fine", Advisory: true}}}},
		Findings: []validation.SourcedFinding{{Validator: "ref", Finding: validation.Finding{Severity: "major", Message: "nil check missing", Criterion: "AC-1", File: "a.go", Line: three()}}},
		Risks:    []validation.SourcedRisk{{Validator: "second", Advisory: true, Risk: validation.Risk{Area: "security", Message: "touches auth", HumanRequired: true}}},
		Validators: []validation.ValidatorOutcome{
			{Name: "ref", Outcome: validation.Fix, Summary: "one test fails", Usage: &validation.Usage{Model: "m", InputTokens: 10, OutputTokens: 2}},
			{Name: "second", Outcome: validation.Approve, Summary: "fine by me", Advisory: true},
		}}
	return run.Run{ID: "run_1", ChangeID: "c_1", Branch: "feat", Iteration: 2, Head: "h1", Base: "b1", Status: run.Finished, Outcome: change.FixRequested, Result: res, StartedAt: t0}
}

// TestValidatorCheckRun: the check run carries the Checks API fields, the
// conclusion from the outcome, the finding as an annotation and the
// verdict embedded; the embedded verdict is valid against
// verdict.schema.json and parses back field for field.
func TestValidatorCheckRun(t *testing.T) {
	r := finishedRun()
	now := t0.Add(time.Minute)
	c, err := ValidatorCheck(r, r.Result.Validators[0], now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "plumbix/ref" || c.HeadSHA != "h1" || c.Status != "completed" || c.Conclusion != "failure" || c.ExternalID != "run_1" || c.CompletedAt == nil || !c.CompletedAt.Equal(now) {
		t.Errorf("check = %+v", c)
	}
	if c.Output.Title != "Fixes requested" || !strings.Contains(c.Output.Summary, "| AC-1 | unmet | step test failed |") || !strings.Contains(c.Output.Summary, "major: nil check missing (a.go:3)") || !strings.Contains(c.Output.Summary, "cost not reported") {
		t.Errorf("output = %+v", c.Output)
	}
	if len(c.Output.Annotations) != 1 || c.Output.Annotations[0].Path != "a.go" || c.Output.Annotations[0].StartLine != 3 || c.Output.Annotations[0].Level != "failure" {
		t.Errorf("annotations = %+v", c.Output.Annotations)
	}
	marker, raw, ok := Extract(c.Output.Text)
	if !ok || marker != VerdictMarker {
		t.Fatalf("embedded: %v %q\n%s", ok, marker, c.Output.Text)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := verdictSchema(t).Validate(doc); err != nil {
		t.Fatalf("embedded verdict does not match verdict.schema.json: %v\n%s", err, raw)
	}
	var v validation.Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Outcome != validation.Fix || v.Summary != "one test fails" || len(v.Criteria) != 1 || v.Criteria[0].Status != validation.Unmet || len(v.Findings) != 1 || v.Findings[0].File != "a.go" || v.Usage == nil || v.Usage.Model != "m" || len(v.Risks) != 0 {
		t.Errorf("parsed verdict = %+v", v)
	}
	if strings.Contains(string(raw), "cost_usd") {
		t.Errorf("a cost the validator never reported was embedded:\n%s", raw)
	}

	adv, err := ValidatorCheck(r, r.Result.Validators[1], now)
	if err != nil || adv.Conclusion != "neutral" || adv.Output.Title != "Advisory: Approved" || !strings.Contains(adv.Output.Summary, "security: touches auth (a person must look)") {
		t.Errorf("advisory check = %+v, %v", adv, err)
	}
	if _, raw, ok := Extract(adv.Output.Text); !ok || !strings.Contains(string(raw), `"human_required": true`) || strings.Contains(string(raw), "nil check") {
		t.Errorf("advisory verdict carries the wrong validator's data:\n%s", raw)
	}

	none, err := ValidatorCheck(r, validation.ValidatorOutcome{Name: "broken", Error: "no verdict file"}, now)
	if err != nil || none.Conclusion != "action_required" || none.Output.Text != "" || !strings.Contains(none.Output.Summary, "no verdict file") {
		t.Errorf("no-verdict check = %+v, %v", none, err)
	}
}

// TestGateCheck: the gate's conclusion follows the state, and its text
// embeds the combined result; without criteria it says so.
func TestGateCheck(t *testing.T) {
	r := finishedRun()
	c := change.Change{ID: "c_1", AcceptanceCriteria: []change.Criterion{{ID: "AC-1", Text: "Tests pass"}}}
	g, err := GateCheck(c, r, t0)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != GateName || g.Conclusion != "failure" || g.Output.Title != "Fixes requested" || !strings.Contains(g.Output.Summary, "| AC-1 | unmet | Tests pass |") || !strings.Contains(g.Output.Summary, "| second | approve (advisory) | plumbix/second |") {
		t.Errorf("gate = %+v", g)
	}
	marker, raw, ok := Extract(g.Output.Text)
	if !ok || marker != ResultMarker {
		t.Fatalf("embedded: %v %q", ok, marker)
	}
	var res validation.Result
	if err := json.Unmarshal(raw, &res); err != nil || res.State != change.FixRequested || len(res.Validators) != 2 || res.Validators[0].Usage == nil {
		t.Errorf("parsed result = %+v, %v", res, err)
	}

	for state, want := range map[change.State][2]string{
		change.Approved: {"success", "Approved"}, change.Rejected: {"failure", "Rejected"},
		change.NeedsHuman: {"action_required", "Needs a person"}, change.Escalated: {"action_required", "Escalated: needs a person"},
	} {
		r.Result.State = state
		r.Result.Criteria = nil
		g, _ := GateCheck(c, r, t0)
		if g.Conclusion != want[0] || g.Output.Title != want[1] || !strings.Contains(g.Output.Summary, NoCriteriaNote) {
			t.Errorf("%s: %s %q\n%s", state, g.Conclusion, g.Output.Title, g.Output.Summary)
		}
	}

	errored := run.Run{ID: "run_2", Head: "h1", Status: run.Errored, Error: "fetch failed", StartedAt: t0}
	g, _ = GateCheck(c, errored, t0)
	if g.Conclusion != "action_required" || !strings.Contains(g.Output.Summary, "fetch failed") || g.Output.Text != "" {
		t.Errorf("errored gate = %+v", g)
	}

	running := GateRunning(r)
	if running.Status != "in_progress" || running.Conclusion != "" || running.HeadSHA != "h1" || running.Output.Title != "Validating" {
		t.Errorf("running gate = %+v", running)
	}
	if _, ok := GateApproved(c, t0); ok {
		t.Error("a gate was built without an approval")
	}
	c.HumanApproval = &change.HumanApproval{Reason: "known flake", Head: "h1", At: t0}
	ap, ok := GateApproved(c, t0)
	if !ok || ap.Conclusion != "success" || ap.HeadSHA != "h1" || !strings.Contains(ap.Output.Summary, "known flake") {
		t.Errorf("approved gate = %+v, %v", ap, ok)
	}
}

// TestEmbeddedVerdictRoundTrip: the markers survive surrounding text and
// a too-large document is replaced by a note rather than cut.
func TestEmbeddedVerdictRoundTrip(t *testing.T) {
	text, err := Embed(VerdictMarker, map[string]any{"plumbix": "verdict/v1", "outcome": "approve", "summary": "s"})
	if err != nil {
		t.Fatal(err)
	}
	marker, raw, ok := Extract("preamble\n" + text + "\ntrailer")
	if !ok || marker != VerdictMarker || !json.Valid(raw) {
		t.Fatalf("round trip: %v %q %s", ok, marker, raw)
	}
	big, err := Embed(ResultMarker, map[string]any{"pad": strings.Repeat("x", maxText)})
	if err != nil || !strings.Contains(big, "omitted") || len(big) > 200 {
		t.Errorf("oversized: %v %q", err, big)
	}
	if _, _, ok := Extract("no block here"); ok {
		t.Error("found a block in plain text")
	}
}
