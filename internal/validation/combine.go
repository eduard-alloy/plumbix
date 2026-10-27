package validation

import (
	"fmt"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// PipelineDir is the folder whose changes always need a human.
const PipelineDir = ".plumbix/"

// ValidatorResult is one validator step's output: a checked verdict, or an
// error when the step crashed or wrote no valid verdict.
type ValidatorResult struct {
	Name    string
	Verdict *Verdict
	Err     error
	// Advisory marks a validator whose verdict is recorded but never
	// counted (WU-030, A31).
	Advisory bool
}

// Result is the combined outcome of an iteration's validators.
type Result struct {
	State      change.State        `json:"state"`
	Criteria   []CombinedCriterion `json:"criteria"`
	Findings   []SourcedFinding    `json:"findings"`
	Risks      []SourcedRisk       `json:"risks"`
	Reasons    []string            `json:"reasons"`    // why the change was rejected or needs a human
	Validators []ValidatorOutcome  `json:"validators"` // what each validator decided, in pipeline order
}

// ValidatorOutcome is one validator's own decision, kept apart from how
// its step ran: a validator that asks for fixes has still run fine.
type ValidatorOutcome struct {
	Name     string  `json:"name"`
	Outcome  Outcome `json:"outcome,omitempty"` // empty when the validator produced no verdict
	Summary  string  `json:"summary,omitempty"`
	Error    string  `json:"error,omitempty"`    // why there is no verdict
	Advisory bool    `json:"advisory,omitempty"` // recorded, never counted
	// Usage is the model usage the validator reported in its verdict
	// (WU-053), copied as given: Plumbix never computes, converts or
	// estimates any of it, cost included. Absent when the validator
	// reported none.
	Usage *Usage `json:"usage,omitempty"`
}

// CombinedCriterion is one criterion's status across validators.
type CombinedCriterion struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Status      Status       `json:"status"`
	Assessments []Assessment `json:"assessments"`
}

// Assessment is one validator's view of a criterion.
type Assessment struct {
	Validator string   `json:"validator"`
	Status    Status   `json:"status"`
	Reason    string   `json:"reason"`
	Evidence  []string `json:"evidence,omitempty"`
	Advisory  bool     `json:"advisory,omitempty"` // shown, not merged into the criterion's status
}

// SourcedFinding is a finding with the validator that reported it.
type SourcedFinding struct {
	Validator string `json:"validator"`
	Advisory  bool   `json:"advisory,omitempty"`
	Finding
}

// SourcedRisk is a risk with the validator that reported it.
type SourcedRisk struct {
	Validator string `json:"validator"`
	Advisory  bool   `json:"advisory,omitempty"`
	Risk
}

// Combine merges validator results into a change outcome (WU-005 rules).
// required lists the validators the target branch's pipeline demands, and
// changedFiles are the files the change touches. An advisory result
// (WU-030, A31) is carried in full, tagged, and moves nothing: not the
// state, not a criterion's status, not the need for a human.
func Combine(criteria []change.Criterion, results []ValidatorResult, required, changedFiles []string) Result {
	r := Result{Criteria: []CombinedCriterion{}, Findings: []SourcedFinding{}, Risks: []SourcedRisk{}, Reasons: []string{}, Validators: []ValidatorOutcome{}}
	byID := map[string]*CombinedCriterion{}
	for _, c := range criteria {
		r.Criteria = append(r.Criteria, CombinedCriterion{ID: c.ID, Text: c.Text, Status: Unknown})
	}
	for i := range r.Criteria {
		byID[r.Criteria[i].ID] = &r.Criteria[i]
	}

	var rejected, fix bool
	var human []string
	counted := 0
	reported := map[string]bool{}
	for _, res := range results {
		if !res.Advisory {
			// Only a counting verdict satisfies the target's required list:
			// a branch cannot defuse a required validator by marking it advisory.
			reported[res.Name] = true
			counted++
		}
		if res.Err != nil || res.Verdict == nil {
			why := "no verdict file"
			if res.Err != nil {
				why = res.Err.Error()
			}
			r.Validators = append(r.Validators, ValidatorOutcome{Name: res.Name, Error: why, Advisory: res.Advisory})
			if !res.Advisory {
				human = append(human, fmt.Sprintf("validator %s produced no valid verdict: %s", res.Name, why))
			}
			continue
		}
		v := res.Verdict
		r.Validators = append(r.Validators, ValidatorOutcome{Name: res.Name, Outcome: v.Outcome, Summary: v.Summary, Advisory: res.Advisory, Usage: v.Usage})
		if !res.Advisory {
			switch v.Outcome {
			case Reject:
				rejected = true
				r.Reasons = append(r.Reasons, fmt.Sprintf("validator %s rejected the change: %s", res.Name, v.Summary))
			case Fix:
				fix = true
			}
		}
		for _, cv := range v.Criteria {
			c, ok := byID[cv.ID]
			if !ok {
				continue
			}
			c.Assessments = append(c.Assessments, Assessment{Validator: res.Name, Status: cv.Status, Reason: cv.Reason, Evidence: cv.Evidence, Advisory: res.Advisory})
		}
		for _, f := range v.Findings {
			r.Findings = append(r.Findings, SourcedFinding{Validator: res.Name, Advisory: res.Advisory, Finding: f})
		}
		for _, rk := range v.Risks {
			r.Risks = append(r.Risks, SourcedRisk{Validator: res.Name, Advisory: res.Advisory, Risk: rk})
			if rk.HumanRequired && !res.Advisory {
				human = append(human, fmt.Sprintf("validator %s flagged a risk that needs a human (%s): %s", res.Name, rk.Area, rk.Message))
			}
		}
	}

	var unknown []string
	for i := range r.Criteria {
		c := &r.Criteria[i]
		c.Status = mergeStatus(c.Assessments)
		switch c.Status {
		case Unmet:
			fix = true
		case Unknown:
			unknown = append(unknown, c.ID)
		}
	}

	if counted == 0 && len(required) == 0 {
		human = append(human, "no validator ran")
	}
	for _, name := range required {
		if !reported[name] {
			human = append(human, fmt.Sprintf("required validator %s did not report", name))
		}
	}
	if len(unknown) > 0 {
		human = append(human, "no validator could assess "+strings.Join(unknown, ", "))
	}
	for _, f := range changedFiles {
		if strings.HasPrefix(f, PipelineDir) {
			human = append(human, "the change edits "+PipelineDir+", which always needs a human")
			break
		}
	}

	switch {
	case rejected:
		r.State = change.Rejected
	case fix:
		r.State = change.FixRequested
	case len(human) > 0:
		r.State = change.NeedsHuman
		r.Reasons = append(r.Reasons, human...)
	default:
		r.State = change.Approved
	}
	return r
}

// mergeStatus: unmet if any counting validator says unmet, met if any says
// met, otherwise unknown. Advisory assessments are shown, not merged.
func mergeStatus(as []Assessment) Status {
	status := Unknown
	for _, a := range as {
		if a.Advisory {
			continue
		}
		switch a.Status {
		case Unmet:
			return Unmet
		case Met:
			status = Met
		}
	}
	return status
}
