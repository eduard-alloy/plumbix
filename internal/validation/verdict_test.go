package validation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

var criteria = []change.Criterion{{ID: "AC-1", Text: "a"}, {ID: "AC-2", Text: "b"}}

func writeVerdict(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verdict.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const goodVerdict = `{
  "plumbix": "verdict/v1",
  "outcome": "fix",
  "summary": "AC-2 is not covered",
  "criteria": [
    {"id": "AC-1", "status": "met", "reason": "step test passed", "evidence": ["test/log.txt"]},
    {"id": "AC-2", "status": "unmet", "reason": "no test for timeouts"}
  ],
  "findings": [{"severity": "major", "criterion": "AC-2", "message": "Add a timeout test", "file": "run.go", "line": 88}],
  "risks": [{"area": "tests", "message": "2 test files changed", "human_required": false}],
  "usage": {"model": "claude-opus-5-5", "input_tokens": 1200, "output_tokens": 300}
}`

func TestReadVerdictValid(t *testing.T) {
	v, err := ReadVerdict(writeVerdict(t, goodVerdict), criteria)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != Fix || len(v.Criteria) != 2 || v.Criteria[1].Status != Unmet || v.Findings[0].Line == nil || *v.Findings[0].Line != 88 || v.Usage.InputTokens != 1200 {
		t.Errorf("verdict = %+v", v)
	}
}

func TestReadVerdictMissingFile(t *testing.T) {
	_, err := ReadVerdict(filepath.Join(t.TempDir(), "verdict.json"), criteria)
	if err == nil || !strings.Contains(err.Error(), "no verdict") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadVerdictRejects(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"not json":           {goodVerdict, `{"plumbix":`, "JSON"},
		"unknown field":      {`"outcome": "fix",`, `"outcome": "fix", "mood": "happy",`, "mood"},
		"wrong version":      {`"verdict/v1"`, `"verdict/v2"`, "verdict/v1"},
		"bad outcome":        {`"outcome": "fix"`, `"outcome": "maybe"`, "outcome"},
		"empty summary":      {`"summary": "AC-2 is not covered"`, `"summary": ""`, "summary"},
		"bad status":         {`"status": "met"`, `"status": "sort of"`, "status"},
		"unknown criterion":  {`"id": "AC-1"`, `"id": "AC-9"`, "AC-9"},
		"duplicate":          {`"id": "AC-1"`, `"id": "AC-2"`, "more than once"},
		"bad severity":       {`"severity": "major"`, `"severity": "meh"`, "severity"},
		"empty finding":      {`"message": "Add a timeout test"`, `"message": ""`, "message"},
		"bad line":           {`"line": 88`, `"line": 0`, "line"},
		"finding bad crit":   {`"criterion": "AC-2"`, `"criterion": "AC-5"`, "AC-5"},
		"risk without area":  {`"area": "tests"`, `"area": ""`, "area"},
		"approve with unmet": {`"outcome": "fix"`, `"outcome": "approve"`, "approve"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(goodVerdict, tc.from, tc.to, 1)
			if tc.from == goodVerdict {
				src = tc.to
			}
			_, err := ReadVerdict(writeVerdict(t, src), criteria)
			var inv *InvalidVerdictError
			if !errors.As(err, &inv) {
				t.Fatalf("err = %v, want *InvalidVerdictError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestFixNeedsAReason(t *testing.T) {
	src := `{"plumbix": "verdict/v1", "outcome": "fix", "summary": "s", "criteria": [{"id": "AC-1", "status": "met", "reason": "ok"}], "findings": [], "risks": []}`
	if _, err := ReadVerdict(writeVerdict(t, src), criteria); err == nil || !strings.Contains(err.Error(), "fix") {
		t.Fatalf("err = %v, want a fix without findings or unmet criteria to be rejected", err)
	}
}

func TestMinimalApprove(t *testing.T) {
	src := `{"plumbix": "verdict/v1", "outcome": "approve", "summary": "all good", "criteria": [], "findings": [], "risks": []}`
	if _, err := ReadVerdict(writeVerdict(t, src), criteria); err != nil {
		t.Fatalf("minimal approve rejected: %v", err)
	}
}

func TestUsageMayCarryTurnsAndCost(t *testing.T) {
	raw := []byte(`{"plumbix":"verdict/v1","outcome":"approve","summary":"ok","criteria":[{"id":"AC-1","status":"met","reason":"r","evidence":["x"]}],"usage":{"model":"m","input_tokens":1,"output_tokens":2,"turns":13,"cost_usd":3.06}}`)
	v, err := ParseVerdict(raw, []change.Criterion{{ID: "AC-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.Turns != 13 || v.Usage.CostUSD != 3.06 {
		t.Errorf("usage = %+v", v.Usage)
	}
	// Absent, they stay out of the JSON a container validator writes.
	out, _ := json.Marshal(Verdict{Plumbix: VerdictVersion, Outcome: "approve", Summary: "ok", Usage: &Usage{Model: "m", InputTokens: 1, OutputTokens: 2}})
	if strings.Contains(string(out), "turns") || strings.Contains(string(out), "cost_usd") {
		t.Errorf("empty usage extras were written: %s", out)
	}
}
