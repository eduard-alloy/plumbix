// Package pullrequest is the pull-request face of the GitHub host (WU-054):
// pull requests become changes, verdicts become check runs, a review
// approval counts as a person's approval, and GitHub's merge is recorded.
package pullrequest

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// CriteriaHeading is the heading, at any level and in any case, under
// which a pull request body lists its acceptance criteria.
const CriteriaHeading = "acceptance criteria"

var (
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	itemRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*\S)\s*$`)
	// bindRe is the one form that binds an item to pipeline steps (WU-062):
	// a trailing "(check: build, test)".
	bindRe = regexp.MustCompile(`(?i)\s*\(check:\s*([^)]*)\)\s*$`)
	// commandRe is what a command binding would look like. The parser knows
	// it only to say no: a pull request author must never run a command on
	// the validator.
	commandRe = regexp.MustCompile(`(?i)\((?:run|command|cmd|exec|shell):[^)]*\)\s*$`)
)

// NoteCommand is the note on an item that asked for a command.
const NoteCommand = "names a command, which a pull request cannot run; bind steps with (check: step, step)"

// ParseCriteria reads the acceptance criteria a pull request body states:
// the list items (bullets, numbers, checklists) under a heading named
// "Acceptance criteria", until the next heading. Lines that continue an
// item are joined to it. Nothing is invented: a body without the heading
// yields no criteria.
//
// An item ending in "(check: build, test)" binds to those pipeline steps
// as check.steps, the parenthesis removed from its text (WU-062). known
// says whether a step exists in the pipeline at the pull request's head;
// nil checks names for form only. A name that is not a step name, or not
// a known step, leaves the item text with a note saying so, and so does
// an item that asks for a command. Nothing is dropped.
func ParseCriteria(body string, known func(step string) bool) []change.Criterion {
	out := parseItems(body)
	for i := range out {
		bind(&out[i], known)
	}
	return out
}

// bind applies the binding form to one item's text.
func bind(c *change.Criterion, known func(step string) bool) {
	if commandRe.MatchString(c.Text) {
		c.Note = NoteCommand
		return
	}
	m := bindRe.FindStringSubmatchIndex(c.Text)
	if m == nil {
		return
	}
	var steps []string
	for _, name := range strings.Split(c.Text[m[2]:m[3]], ",") {
		if name = strings.TrimSpace(name); name != "" {
			steps = append(steps, name)
		}
	}
	if len(steps) == 0 {
		return // "(check: )" says nothing; the item stays as written
	}
	text := strings.TrimSpace(c.Text[:m[0]])
	if text == "" {
		return // an item that is only a binding has no criterion to bind
	}
	var bad, unknown []string
	for _, s := range steps {
		switch {
		case !pipeline.ValidStepName(s):
			bad = append(bad, s)
		case known != nil && !known(s):
			unknown = append(unknown, s)
		}
	}
	c.Text = text
	switch {
	case len(bad) > 0:
		c.Note = "names `" + strings.Join(bad, "`, `") + "`, which is not a step name; a pull request binds steps only, never a command"
	case len(unknown) == 1:
		c.Note = "names step `" + unknown[0] + "`, which the pipeline has not"
	case len(unknown) > 1:
		c.Note = "names steps `" + strings.Join(unknown, "`, `") + "`, which the pipeline has not"
	default:
		c.Check = &change.Check{Steps: steps}
	}
}

// parseItems reads the items under the heading, as text.
func parseItems(body string) []change.Criterion {
	var out []change.Criterion
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			in = strings.EqualFold(strings.TrimSpace(m[1]), CriteriaHeading)
			continue
		}
		if !in {
			continue
		}
		if m := itemRe.FindStringSubmatch(line); m != nil {
			if len(out) >= change.MaxCriteria {
				break
			}
			out = append(out, change.Criterion{Text: truncate(m[1], change.MaxText)})
			continue
		}
		if t := strings.TrimSpace(line); t != "" && len(out) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			out[len(out)-1].Text = truncate(out[len(out)-1].Text+" "+t, change.MaxText)
		}
	}
	for i := range out {
		out[i].ID = "AC-" + itoa(i+1)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// truncate keeps at most max runes.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
