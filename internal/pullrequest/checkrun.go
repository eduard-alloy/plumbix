package pullrequest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Check-run names and the markers around the structured blocks in
// output.text. Documented in contracts/github-check-runs.md.
const (
	CheckPrefix = "plumbix/"
	GateName    = CheckPrefix + "gate"
	// VerdictMarker opens one validator's verdict, a verdict.schema.json
	// object; ResultMarker opens the gate's combined run result, the
	// OpenAPI ValidationResult object.
	VerdictMarker = "plumbix-verdict/v1"
	ResultMarker  = "plumbix-result/v1"
	// NoCriteriaNote is what the gate says when the pull request stated no
	// acceptance criteria.
	NoCriteriaNote = "No acceptance criteria stated in the pull request: validators judged the intent and the diff only."
)

// GitHub's limits on a check run.
const (
	maxText        = 65535
	maxSummary     = 65535
	maxAnnotations = 50
)

var embeddedRe = regexp.MustCompile("(?s)<!-- (plumbix-[a-z]+/v[0-9]+) -->\\s*```json\\n(.*?)\\n```\\s*<!-- /plumbix-[a-z]+ -->")

// Embed wraps a JSON document between the markers a reader looks for.
func Embed(marker string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	name := strings.SplitN(marker, "/", 2)[0]
	text := fmt.Sprintf("<!-- %s -->\n```json\n%s\n```\n<!-- /%s -->\n", marker, data, name)
	if len(text) > maxText {
		return fmt.Sprintf("<!-- %s omitted: %d bytes exceed GitHub's limit; read it from the Plumbix API -->\n", marker, len(text)), nil
	}
	return text, nil
}

// Extract finds the embedded block in a check run's output.text and
// returns its marker and JSON.
func Extract(text string) (marker string, raw []byte, ok bool) {
	m := embeddedRe.FindStringSubmatch(text)
	if m == nil {
		return "", nil, false
	}
	return m[1], []byte(m[2]), true
}

// ValidatorName is the check-run name of a validator.
func ValidatorName(validator string) string { return CheckPrefix + validator }

// verdictDoc is a validator's verdict as verdict.schema.json shapes it,
// rebuilt from the run result: the result keeps each validator's
// criteria, findings, risks and usage apart, so nothing is invented.
type verdictDoc struct {
	Plumbix  string                        `json:"plumbix"`
	Outcome  validation.Outcome            `json:"outcome"`
	Summary  string                        `json:"summary"`
	Criteria []validation.CriterionVerdict `json:"criteria,omitempty"`
	Findings []validation.Finding          `json:"findings,omitempty"`
	Risks    []validation.Risk             `json:"risks,omitempty"`
	Usage    *validation.Usage             `json:"usage,omitempty"`
}

// verdictOf rebuilds one validator's verdict from the combined result.
func verdictOf(res *validation.Result, v validation.ValidatorOutcome) verdictDoc {
	d := verdictDoc{Plumbix: validation.VerdictVersion, Outcome: v.Outcome, Summary: v.Summary, Usage: v.Usage}
	for _, c := range res.Criteria {
		for _, a := range c.Assessments {
			if a.Validator == v.Name {
				d.Criteria = append(d.Criteria, validation.CriterionVerdict{ID: c.ID, Status: a.Status, Reason: a.Reason, Evidence: a.Evidence})
			}
		}
	}
	for _, f := range res.Findings {
		if f.Validator == v.Name {
			d.Findings = append(d.Findings, f.Finding)
		}
	}
	for _, r := range res.Risks {
		if r.Validator == v.Name {
			d.Risks = append(d.Risks, r.Risk)
		}
	}
	return d
}

// ValidatorCheck is the completed check run for one validator's verdict.
func ValidatorCheck(r run.Run, v validation.ValidatorOutcome, now time.Time) (host.CheckRun, error) {
	c := host.CheckRun{Name: ValidatorName(v.Name), HeadSHA: r.Head, Status: "completed", ExternalID: r.ID, CompletedAt: &now, StartedAt: &r.StartedAt}
	out := &host.CheckOutput{}
	c.Output = out
	if v.Error != "" || v.Outcome == "" {
		c.Conclusion = "action_required"
		out.Title = "No verdict"
		out.Summary = fmt.Sprintf("Validator %s produced no valid verdict: %s", v.Name, v.Error)
		if v.Advisory {
			c.Conclusion = "neutral"
			out.Title = "Advisory: no verdict"
		}
		return c, nil
	}
	doc := verdictOf(r.Result, v)
	switch {
	case v.Advisory:
		c.Conclusion = "neutral"
		out.Title = "Advisory: " + outcomeWord(v.Outcome)
	case v.Outcome == validation.Approve:
		c.Conclusion = "success"
		out.Title = outcomeWord(v.Outcome)
	default:
		c.Conclusion = "failure"
		out.Title = outcomeWord(v.Outcome)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", v.Summary)
	if v.Advisory {
		b.WriteString("\nThis validator is advisory: its verdict is recorded and never counted.\n")
	}
	if len(doc.Criteria) > 0 {
		b.WriteString("\n| Criterion | Status | Reason |\n|---|---|---|\n")
		for _, cv := range doc.Criteria {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cv.ID, cv.Status, cell(cv.Reason))
		}
	}
	if len(doc.Findings) > 0 {
		b.WriteString("\nFindings:\n")
		for _, f := range doc.Findings {
			fmt.Fprintf(&b, "- %s: %s%s\n", f.Severity, f.Message, where(f))
			if f.File != "" && f.Line != nil && len(out.Annotations) < maxAnnotations {
				out.Annotations = append(out.Annotations, host.Annotation{Path: f.File, StartLine: *f.Line, EndLine: *f.Line, Level: level(f.Severity), Message: f.Message, Title: v.Name + ": " + f.Severity})
			}
		}
	}
	if len(doc.Risks) > 0 {
		b.WriteString("\nRisks:\n")
		for _, rk := range doc.Risks {
			human := ""
			if rk.HumanRequired {
				human = " (a person must look)"
			}
			fmt.Fprintf(&b, "- %s: %s%s\n", rk.Area, rk.Message, human)
		}
	}
	if v.Usage != nil {
		fmt.Fprintf(&b, "\nModel usage as the validator reported it: %s, %d input and %d output tokens", v.Usage.Model, v.Usage.InputTokens, v.Usage.OutputTokens)
		if cost, ok := reportedCost(v.Usage); ok {
			fmt.Fprintf(&b, ", $%.4f", cost)
		} else {
			b.WriteString(", cost not reported")
		}
		b.WriteString(".\n")
	}
	out.Summary = clip(b.String(), maxSummary)
	text, err := Embed(VerdictMarker, doc)
	if err != nil {
		return host.CheckRun{}, err
	}
	out.Text = text
	return c, nil
}

// GateCheck is the summary check run for a finished run of a change.
func GateCheck(c change.Change, r run.Run, now time.Time) (host.CheckRun, error) {
	check := host.CheckRun{Name: GateName, HeadSHA: r.Head, Status: "completed", ExternalID: r.ID, CompletedAt: &now, StartedAt: &r.StartedAt}
	out := &host.CheckOutput{}
	check.Output = out
	if r.Status != run.Finished || r.Result == nil {
		check.Conclusion = "action_required"
		out.Title = "Run " + string(r.Status)
		out.Summary = fmt.Sprintf("Run %s (iteration %d) %s: %s\n\nThe change needs a person.", r.ID, r.Iteration, r.Status, r.Error)
		return check, nil
	}
	res := r.Result
	check.Conclusion, out.Title = gateConclusion(res.State)
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s, iteration %d: the change is %s.\n", r.ID, r.Iteration, res.State)
	if len(res.Reasons) > 0 {
		b.WriteString("\n")
		for _, reason := range res.Reasons {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
	}
	if len(res.Criteria) == 0 {
		b.WriteString("\n" + NoCriteriaNote + "\n")
	} else {
		// The notes and the split come from the change's criteria as the run
		// judged them: an item the pull request edited since keeps the run's
		// row and gets no note, so the table never mixes two versions.
		judged := map[string]string{}
		for _, cr := range res.Criteria {
			judged[cr.ID] = cr.Text
		}
		notes := map[string]string{}
		bound := 0
		for _, ac := range c.AcceptanceCriteria {
			if text, ok := judged[ac.ID]; !ok || text != ac.Text {
				continue
			}
			if ac.Note != "" {
				notes[ac.ID] = ac.Note
			}
			if ac.Check != nil {
				bound++
			}
		}
		// How the criteria split between the deterministic validator and the
		// AI review (WU-062), so a reader knows what "unknown" means here.
		fmt.Fprintf(&b, "\n%s bound to pipeline steps, %d text for the AI review.\n", plural(bound, "criterion", "criteria"), len(res.Criteria)-bound)
		b.WriteString("\n| Criterion | Status | Text |\n|---|---|---|\n")
		for _, cr := range res.Criteria {
			text := cr.Text
			if n := notes[cr.ID]; n != "" {
				text += " (note: " + n + ")"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cr.ID, cr.Status, cell(text))
		}
	}
	if len(res.Validators) > 0 {
		b.WriteString("\n| Validator | Outcome | Check run |\n|---|---|---|\n")
		for _, v := range res.Validators {
			outcome := string(v.Outcome)
			if v.Error != "" {
				outcome = "no verdict"
			}
			if v.Advisory {
				outcome += " (advisory)"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", v.Name, outcome, ValidatorName(v.Name))
		}
	}
	if n := len(res.Findings); n > 0 {
		fmt.Fprintf(&b, "\n%d finding(s); each validator's check run lists its own.\n", n)
	}
	out.Summary = clip(b.String(), maxSummary)
	text, err := Embed(ResultMarker, res)
	if err != nil {
		return host.CheckRun{}, err
	}
	out.Text = text
	return check, nil
}

// GateRunning is the gate while a run goes: the hold.
func GateRunning(r run.Run) host.CheckRun {
	return host.CheckRun{Name: GateName, HeadSHA: r.Head, Status: "in_progress", ExternalID: r.ID, StartedAt: &r.StartedAt,
		Output: &host.CheckOutput{Title: "Validating", Summary: fmt.Sprintf("Run %s, iteration %d, is validating this commit.", r.ID, r.Iteration)}}
}

// GateApproved is the gate after a person approved the change at head;
// ok is false when no approval stands.
func GateApproved(c change.Change, now time.Time) (host.CheckRun, bool) {
	a := c.HumanApproval
	if a == nil || a.Head == "" {
		return host.CheckRun{}, false
	}
	return host.CheckRun{Name: GateName, HeadSHA: a.Head, Status: "completed", Conclusion: "success", ExternalID: c.ID, CompletedAt: &now,
		Output: &host.CheckOutput{Title: "Approved by a person", Summary: fmt.Sprintf("A person approved change %s at %s.\n\nReason: %s", c.ID, a.Head, a.Reason)}}, true
}

func gateConclusion(s change.State) (conclusion, title string) {
	switch s {
	case change.Approved:
		return "success", "Approved"
	case change.FixRequested:
		return "failure", "Fixes requested"
	case change.Rejected:
		return "failure", "Rejected"
	case change.Escalated:
		return "action_required", "Escalated: needs a person"
	default:
		return "action_required", "Needs a person"
	}
}

func outcomeWord(o validation.Outcome) string {
	switch o {
	case validation.Approve:
		return "Approved"
	case validation.Fix:
		return "Fixes requested"
	case validation.Reject:
		return "Rejected"
	}
	return string(o)
}

func level(severity string) string {
	if severity == "minor" {
		return "warning"
	}
	return "failure"
}

func where(f validation.Finding) string {
	switch {
	case f.File != "" && f.Line != nil:
		return fmt.Sprintf(" (%s:%d)", f.File, *f.Line)
	case f.File != "":
		return " (" + f.File + ")"
	}
	return ""
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// plural counts in words: "1 criterion", "2 criteria".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
