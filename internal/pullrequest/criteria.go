// Package pullrequest is the pull-request face of the GitHub host (WU-054):
// pull requests become changes, verdicts become check runs, a review
// approval counts as a person's approval, and GitHub's merge is recorded.
package pullrequest

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// CriteriaHeading is the heading, at any level and in any case, under
// which a pull request body lists its acceptance criteria.
const CriteriaHeading = "acceptance criteria"

var (
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	itemRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*\S)\s*$`)
)

// ParseCriteria reads the acceptance criteria a pull request body states:
// the list items (bullets, numbers, checklists) under a heading named
// "Acceptance criteria", until the next heading. Lines that continue an
// item are joined to it. Nothing is invented: a body without the heading
// yields no criteria.
func ParseCriteria(body string) []change.Criterion {
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
