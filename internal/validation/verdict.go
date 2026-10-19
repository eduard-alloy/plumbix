// Package validation defines the validator contract: the inputs Plumbix
// gives a validator step, the verdict it must write, and how verdicts from
// several validators combine into a change outcome.
package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// VerdictVersion identifies the verdict format.
const VerdictVersion = "verdict/v1"

// Outcome is a single validator's decision.
type Outcome string

// Validator outcomes.
const (
	Approve Outcome = "approve"
	Fix     Outcome = "fix"
	Reject  Outcome = "reject"
)

// Status is a criterion's assessment.
type Status string

// Criterion statuses.
const (
	Met     Status = "met"
	Unmet   Status = "unmet"
	Unknown Status = "unknown"
)

// Verdict is what a validator writes to /plumbix/out/verdict.json.
type Verdict struct {
	Plumbix  string             `json:"plumbix"`
	Outcome  Outcome            `json:"outcome"`
	Summary  string             `json:"summary"`
	Criteria []CriterionVerdict `json:"criteria,omitempty"`
	Findings []Finding          `json:"findings,omitempty"`
	Risks    []Risk             `json:"risks,omitempty"`
	Usage    *Usage             `json:"usage,omitempty"`
}

// CriterionVerdict is a validator's assessment of one criterion.
type CriterionVerdict struct {
	ID       string   `json:"id"`
	Status   Status   `json:"status"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
}

// Finding is something the author should fix.
type Finding struct {
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Criterion string `json:"criterion,omitempty"`
	File      string `json:"file,omitempty"`
	Line      *int   `json:"line,omitempty"` // nil when the finding has no line
}

// Risk is something a human should look at.
type Risk struct {
	Area          string `json:"area"`
	Message       string `json:"message"`
	HumanRequired bool   `json:"human_required"`
}

// Usage reports model usage for validators that call one.
type Usage struct {
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	// Turns and CostUSD are what an agentic reviewer knows and a one-shot
	// validator does not: agentic turns taken, and the spend as the
	// reviewer's tool estimated it (WU-023).
	Turns   int     `json:"turns,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	// ReasoningTokens are the hidden thinking tokens a reasoning model spent
	// before its answer (WU-034): where a slow review's time went.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// CacheReadTokens and CacheWriteTokens are input tokens read from and
	// written to the provider's prompt cache (WU-039); InputTokens counts
	// only the rest, so the three together are the whole prompt.
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

var severities = map[string]bool{"blocker": true, "major": true, "minor": true}

// InvalidVerdictError lists every rule a verdict breaks.
type InvalidVerdictError struct {
	Problems []string
}

func (e *InvalidVerdictError) Error() string {
	return "invalid verdict: " + strings.Join(e.Problems, "; ")
}

// ReadVerdict reads and checks the verdict at path against the change's
// criteria. Any problem makes the verdict a validator error.
func ReadVerdict(path string, criteria []change.Criterion) (Verdict, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Verdict{}, fmt.Errorf("validator wrote no verdict at %s", path)
	}
	if err != nil {
		return Verdict{}, fmt.Errorf("read verdict: %w", err)
	}
	return ParseVerdict(data, criteria)
}

// ParseVerdict decodes and checks a verdict.
func ParseVerdict(data []byte, criteria []change.Criterion) (Verdict, error) {
	var v Verdict
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return Verdict{}, &InvalidVerdictError{Problems: []string{"not a valid JSON verdict: " + err.Error()}}
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return Verdict{}, &InvalidVerdictError{Problems: []string{"not a valid JSON verdict: more than one JSON value"}}
	}
	if ps := v.check(criteria); len(ps) > 0 {
		return Verdict{}, &InvalidVerdictError{Problems: ps}
	}
	return v, nil
}

func (v Verdict) check(criteria []change.Criterion) []string {
	var ps []string
	add := func(format string, args ...any) { ps = append(ps, fmt.Sprintf(format, args...)) }
	known := map[string]bool{}
	for _, c := range criteria {
		known[c.ID] = true
	}

	if v.Plumbix != VerdictVersion {
		add("plumbix must be %q", VerdictVersion)
	}
	switch v.Outcome {
	case Approve, Fix, Reject:
	default:
		add("outcome must be approve, fix or reject")
	}
	if strings.TrimSpace(v.Summary) == "" {
		add("summary is required")
	}
	seen := map[string]bool{}
	unmet := 0
	for i, c := range v.Criteria {
		switch {
		case !known[c.ID]:
			add("criteria[%d]: %q is not a criterion of this change", i, c.ID)
		case seen[c.ID]:
			add("criteria[%d]: %q appears more than once", i, c.ID)
		}
		seen[c.ID] = true
		switch c.Status {
		case Met, Unknown:
		case Unmet:
			unmet++
		default:
			add("criteria[%d]: status must be met, unmet or unknown", i)
		}
		if strings.TrimSpace(c.Reason) == "" {
			add("criteria[%d]: reason is required", i)
		}
	}
	for i, f := range v.Findings {
		if !severities[f.Severity] {
			add("findings[%d]: severity must be blocker, major or minor", i)
		}
		if strings.TrimSpace(f.Message) == "" {
			add("findings[%d]: message is required", i)
		}
		if f.Criterion != "" && !known[f.Criterion] {
			add("findings[%d]: %q is not a criterion of this change", i, f.Criterion)
		}
		if f.Line != nil && *f.Line < 1 {
			add("findings[%d]: line must be 1 or more", i)
		}
	}
	for i, r := range v.Risks {
		if strings.TrimSpace(r.Area) == "" {
			add("risks[%d]: area is required", i)
		}
		if strings.TrimSpace(r.Message) == "" {
			add("risks[%d]: message is required", i)
		}
	}
	if v.Usage != nil {
		if strings.TrimSpace(v.Usage.Model) == "" {
			add("usage: model is required")
		}
		if v.Usage.InputTokens < 0 || v.Usage.OutputTokens < 0 {
			add("usage: token counts cannot be negative")
		}
	}
	if v.Outcome == Approve && unmet > 0 {
		add("approve cannot include unmet criteria")
	}
	if v.Outcome == Fix && unmet == 0 && len(v.Findings) == 0 {
		add("fix needs at least one finding or unmet criterion")
	}
	return ps
}
