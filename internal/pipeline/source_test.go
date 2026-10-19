package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// TestStepSource (WU-046): an imported step says where it came from.
func TestStepSource(t *testing.T) {
	p, err := ParseTriggered([]byte("version: 1\non: {manual: true}\nsteps:\n  - name: a\n    image: x\n    run: x\n    source: {file: pipelines/ci.yml, line: 12, hash: " + strings.Repeat("a", 64) + "}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Steps[0].Source; s == nil || s.File != "pipelines/ci.yml" || s.Line != 12 || len(s.Hash) != 64 {
		t.Errorf("source = %+v", s)
	}
	for name, c := range map[string]struct{ src, path, msg string }{
		"no file":   {"{line: 1, hash: " + strings.Repeat("a", 64) + "}", "steps[0].source.file", "is required"},
		"bad line":  {"{file: a, line: 0, hash: " + strings.Repeat("a", 64) + "}", "steps[0].source.line", "positive"},
		"bad hash":  {"{file: a, line: 1, hash: xyz}", "steps[0].source.hash", "64 hex"},
		"extra key": {"{file: a, line: 1, hash: " + strings.Repeat("a", 64) + ", by: me}", "steps[0].source.by", "unknown field"},
	} {
		_, err := ParseTriggered([]byte("version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x, source: " + c.src + "}\n"))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: err = %v", name, err)
		}
		found := false
		for _, e := range ve.Errors {
			found = found || (e.Path == c.path && strings.Contains(e.Message, c.msg))
		}
		if !found {
			t.Errorf("%s: no %s %q in %v", name, c.path, c.msg, ve.Errors)
		}
	}
	if _, err := Parse([]byte(wrap("  - {name: a, image: x, run: x, disabled: {reason: variables, note: \"$(Build.BuildId)\"}}\n"))); err != nil {
		t.Errorf("reason variables: %v", err)
	}
}
