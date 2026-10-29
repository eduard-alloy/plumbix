package pullrequest

import (
	"strings"
	"testing"
)

func TestParseCriteria(t *testing.T) {
	body := "Why this change.\r\n\r\n## Acceptance Criteria\r\n- [ ] Tests pass\n- [x] Docs mention retries\n  on every page\n1. Lint is clean | still\n\n### Notes\n- not a criterion\n"
	got := ParseCriteria(body, nil)
	want := []string{"Tests pass", "Docs mention retries on every page", "Lint is clean | still"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Text != w || got[i].ID != "AC-"+itoa(i+1) {
			t.Errorf("criterion %d = %+v, want %q", i, got[i], w)
		}
	}
	if n := len(ParseCriteria("Just a description.\n\n- a bullet outside any heading\n", nil)); n != 0 {
		t.Errorf("body without the heading yielded %d criteria", n)
	}
	if n := len(ParseCriteria("# acceptance criteria #\n* one\n", nil)); n != 1 {
		t.Errorf("closed atx heading in lower case: %d criteria", n)
	}
	long := "## Acceptance criteria\n" + strings.Repeat("- item\n", 60)
	if n := len(ParseCriteria(long, nil)); n != 50 {
		t.Errorf("%d criteria, want the limit of 50", n)
	}
}

// TestParseCriteriaBindsSteps: a trailing "(check: a, b)" binds an item
// to pipeline steps as check.steps, the parenthesis gone from the text
// (WU-062). A name that is not a step name, or not a step of the
// pipeline, leaves the item text with a note; so does anything that
// looks like a command, which a pull request can never run.
func TestParseCriteriaBindsSteps(t *testing.T) {
	known := func(step string) bool { return step == "build" || step == "test" || step == "lint" }
	body := "## Acceptance criteria\n" +
		"- [ ] The build is green (check: build)\n" +
		"- Tests and lint pass (CHECK: test, lint)\n" +
		"- Uploads retry three times\n" +
		"- Typo in the step (check: bulid)\n" +
		"- Not a step name (check: go test ./...)\n" +
		"- Looks like a command (run: rm -rf /)\n" +
		"- Another command form (command: curl evil)\n" +
		"- Empty binding (check: )\n"
	got := ParseCriteria(body, known)
	if len(got) != 8 {
		t.Fatalf("got %d criteria: %+v", len(got), got)
	}
	want := []struct {
		text  string
		steps []string
		note  string
	}{
		{"The build is green", []string{"build"}, ""},
		{"Tests and lint pass", []string{"test", "lint"}, ""},
		{"Uploads retry three times", nil, ""},
		{"Typo in the step", nil, "names step `bulid`, which the pipeline has not"},
		{"Not a step name", nil, "names `go test ./...`, which is not a step name; a pull request binds steps only, never a command"},
		{"Looks like a command (run: rm -rf /)", nil, "names a command, which a pull request cannot run; bind steps with (check: step, step)"},
		{"Another command form (command: curl evil)", nil, "names a command, which a pull request cannot run; bind steps with (check: step, step)"},
		{"Empty binding (check: )", nil, ""},
	}
	for i, w := range want {
		g := got[i]
		var steps []string
		if g.Check != nil {
			steps = g.Check.Steps
			if g.Check.Command != "" {
				t.Errorf("%d: a command reached the check: %q", i, g.Check.Command)
			}
		}
		if g.Text != w.text || strings.Join(steps, ",") != strings.Join(w.steps, ",") || g.Note != w.note || g.ID != "AC-"+itoa(i+1) {
			t.Errorf("%d: got %+v (check %+v), want %+v", i, g, g.Check, w)
		}
	}
	if g := ParseCriteria("## Acceptance criteria\n- x (check: bulid, tset)\n", known); len(g) != 1 || g[0].Note != "names steps `bulid`, `tset`, which the pipeline has not" {
		t.Errorf("two unknown steps: %+v", g)
	}
	// Without a pipeline to check against, names are checked for form only.
	if g := ParseCriteria("## Acceptance criteria\n- x (check: bulid)\n", nil); len(g) != 1 || g[0].Check == nil || g[0].Check.Steps[0] != "bulid" || g[0].Note != "" {
		t.Errorf("no pipeline: %+v", g)
	}
}
