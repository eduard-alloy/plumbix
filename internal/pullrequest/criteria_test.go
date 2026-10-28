package pullrequest

import (
	"strings"
	"testing"
)

func TestParseCriteria(t *testing.T) {
	body := "Why this change.\r\n\r\n## Acceptance Criteria\r\n- [ ] Tests pass\n- [x] Docs mention retries\n  on every page\n1. Lint is clean | still\n\n### Notes\n- not a criterion\n"
	got := ParseCriteria(body)
	want := []string{"Tests pass", "Docs mention retries on every page", "Lint is clean | still"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Text != w || got[i].ID != "AC-"+itoa(i+1) {
			t.Errorf("criterion %d = %+v, want %q", i, got[i], w)
		}
	}
	if n := len(ParseCriteria("Just a description.\n\n- a bullet outside any heading\n")); n != 0 {
		t.Errorf("body without the heading yielded %d criteria", n)
	}
	if n := len(ParseCriteria("# acceptance criteria #\n* one\n")); n != 1 {
		t.Errorf("closed atx heading in lower case: %d criteria", n)
	}
	long := "## Acceptance criteria\n" + strings.Repeat("- item\n", 60)
	if n := len(ParseCriteria(long)); n != 50 {
		t.Errorf("%d criteria, want the limit of 50", n)
	}
}
