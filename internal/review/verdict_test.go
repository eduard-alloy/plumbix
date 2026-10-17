package review

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

var usage = validation.Usage{Model: "claude-opus-5-5", InputTokens: 900, OutputTokens: 120}

func TestToVerdictValid(t *testing.T) {
	raw := `{"outcome": "approve", "summary": "both criteria hold",
	  "criteria": [
	    {"id": "AC-1", "status": "met", "reason": "test step passed", "evidence": ["evidence/test/log.txt"]},
	    {"id": "AC-2", "status": "met", "reason": "README line 4", "evidence": ["README.md:4"]}],
	  "findings": [], "risks": []}`
	v, err := ToVerdict([]byte(raw), testCriteria, usage)
	if err != nil {
		t.Fatal(err)
	}
	if v.Plumbix != validation.VerdictVersion || v.Outcome != validation.Approve || v.Usage == nil || v.Usage.InputTokens != 900 {
		t.Errorf("verdict = %+v", v)
	}
}

func TestMetWithoutEvidenceBecomesUnknown(t *testing.T) {
	raw := `{"outcome": "approve", "summary": "s",
	  "criteria": [{"id": "AC-1", "status": "met", "reason": "looks fine", "evidence": []}],
	  "findings": [], "risks": []}`
	v, err := ToVerdict([]byte(raw), testCriteria, usage)
	if err != nil {
		t.Fatal(err)
	}
	c := v.Criteria[0]
	if c.Status != validation.Unknown || !strings.Contains(c.Reason, "no evidence") {
		t.Errorf("AC-1 = %+v", c)
	}
}

func TestApproveWithUnmetBecomesFix(t *testing.T) {
	raw := `{"outcome": "approve", "summary": "s",
	  "criteria": [{"id": "AC-2", "status": "unmet", "reason": "README unchanged", "evidence": []}],
	  "findings": [{"severity": "major", "message": "document retries", "criterion": "AC-2", "file": "", "line": 0}], "risks": []}`
	v, err := ToVerdict([]byte(raw), testCriteria, usage)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != validation.Fix || v.Findings[0].Line != nil || v.Findings[0].File != "" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestToVerdictErrors(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":    `{"outcome":`,
		"bad outcome": `{"outcome": "shrug", "summary": "s", "criteria": [], "findings": [], "risks": []}`,
	} {
		if _, err := ToVerdict([]byte(raw), testCriteria, usage); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSchemaFitsStructuredOutputs(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			if arr, ok := v.([]any); ok {
				for _, x := range arr {
					walk(path, x)
				}
			}
			return
		}
		for _, banned := range []string{"minLength", "maxLength", "minimum", "maximum", "minItems", "maxItems", "pattern"} {
			if _, has := m[banned]; has {
				t.Errorf("%s uses %s, which structured outputs don't support", path, banned)
			}
		}
		if m["type"] == "object" {
			if m["additionalProperties"] != false {
				t.Errorf("%s: objects need additionalProperties false", path)
			}
			props, _ := m["properties"].(map[string]any)
			req, _ := m["required"].([]string)
			if len(req) != len(props) {
				t.Errorf("%s: every property should be required (%d of %d)", path, len(req), len(props))
			}
		}
		for k, x := range m {
			walk(path+"."+k, x)
		}
	}
	walk("schema", OutputSchema([]string{"AC-1", "AC-2"}))
	walk("schema-no-ids", OutputSchema(nil))
}

// TestSchemaRestrictsCriterionIDs: the model can only name the change's
// own criteria, so it cannot invent one for a constraint.
func TestSchemaRestrictsCriterionIDs(t *testing.T) {
	s := OutputSchema([]string{"AC-1", "AC-2"})
	crit := s["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	id := crit["id"].(map[string]any)
	if got, _ := id["enum"].([]string); len(got) != 2 || got[0] != "AC-1" || got[1] != "AC-2" {
		t.Errorf("id schema = %v, want an enum of the criterion ids", id)
	}
	if _, has := OutputSchema(nil)["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["enum"]; has {
		t.Error("with no criteria the id must stay a plain string")
	}
}

// TestInventedCriteriaAreDropped: a criterion the change does not have is
// dropped with a note, and a repeated id keeps its first entry, instead of
// the whole review failing.
func TestInventedCriteriaAreDropped(t *testing.T) {
	raw := `{"outcome": "fix", "summary": "s", "criteria": [
	  {"id": "AC-1", "status": "met", "reason": "r", "evidence": ["x"]},
	  {"id": "constraint-states", "status": "met", "reason": "r", "evidence": ["x"]},
	  {"id": "AC-1", "status": "unmet", "reason": "again", "evidence": []},
	  {"id": "AC-2", "status": "unmet", "reason": "r", "evidence": []}], "findings": [], "risks": []}`
	v, err := ToVerdict([]byte(raw), testCriteria, usage)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Criteria) != 2 || v.Criteria[0].ID != "AC-1" || v.Criteria[0].Status != "met" || v.Criteria[1].ID != "AC-2" {
		t.Errorf("criteria = %+v", v.Criteria)
	}
	if !strings.Contains(v.Summary, "constraint-states") {
		t.Errorf("summary does not note the dropped criterion: %q", v.Summary)
	}
}

func TestEmptyTextIsNormalizedConservatively(t *testing.T) {
	raw := `{"outcome": "approve", "summary": " ",
	  "criteria": [
	    {"id": "AC-1", "status": "met", "reason": "", "evidence": ["evidence/test/log.txt"]},
	    {"id": "AC-2", "status": "met", "reason": "README line 3", "evidence": ["README.md:3"]}],
	  "findings": [], "risks": []}`
	v, err := ToVerdict([]byte(raw), testCriteria, usage)
	if err != nil {
		t.Fatal(err)
	}
	if v.Criteria[0].Status != validation.Unknown || !strings.Contains(v.Criteria[0].Reason, "no reason") {
		t.Errorf("AC-1 = %+v; a status without a reason must not count", v.Criteria[0])
	}
	if v.Criteria[1].Status != validation.Met {
		t.Errorf("AC-2 = %+v", v.Criteria[1])
	}
	if !strings.Contains(v.Summary, "generated by Plumbix") || !strings.Contains(v.Summary, "1 met") {
		t.Errorf("summary = %q", v.Summary)
	}
}
