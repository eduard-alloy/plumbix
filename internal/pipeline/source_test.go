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

// TestSourceWritten (WU-048): the import stamps what it wrote, on each step
// and on the pipeline, so a re-check can tell its content from a person's.
func TestSourceWritten(t *testing.T) {
	h := strings.Repeat("b", 64)
	p, err := ParseTriggered([]byte("version: 1\non: {manual: true}\nsource: {system: azure, file: ci.yml, written: " + h + "}\nsteps:\n  - name: a\n    image: x\n    run: x\n    source: {file: ci.yml, line: 3, hash: " + h + ", written: " + h + "}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Steps[0].Source; s == nil || s.Written != h {
		t.Errorf("step source = %+v", s)
	}
	if s := p.Imported; s == nil || s.System != "azure" || s.File != "ci.yml" || s.Written != h {
		t.Errorf("pipeline source = %+v", s)
	}
	if p, err := ParseTriggered([]byte("version: 1\non: {manual: true}\nsource: {system: azure, file: ci.yml}\nsteps:\n  - {name: a, image: x, run: x}\n")); err != nil || p.Imported.Written != "" {
		t.Errorf("written is optional: %v", err)
	}
	for name, c := range map[string]struct{ doc, path, msg string }{
		"step bad written": {"steps:\n  - {name: a, image: x, run: x, source: {file: a, line: 1, hash: " + h + ", written: x}}\n", "steps[0].source.written", "64 hex"},
		"no system":        {"source: {file: ci.yml}\nsteps:\n  - {name: a, image: x, run: x}\n", "source.system", "is required"},
		"no file":          {"source: {system: azure}\nsteps:\n  - {name: a, image: x, run: x}\n", "source.file", "is required"},
		"bad written":      {"source: {system: azure, file: a, written: \"12\"}\nsteps:\n  - {name: a, image: x, run: x}\n", "source.written", "64 hex"},
		"extra key":        {"source: {system: azure, file: a, line: 1}\nsteps:\n  - {name: a, image: x, run: x}\n", "source.line", "unknown field"},
		"not a mapping":    {"source: azure\nsteps:\n  - {name: a, image: x, run: x}\n", "source", "mapping"},
	} {
		_, err := ParseTriggered([]byte("version: 1\non: {manual: true}\n" + c.doc))
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
	_, err = Parse([]byte("version: 1\nsource: {system: azure, file: a}\nsteps:\n  - {name: a, image: x, run: x}\n"))
	if err == nil || !strings.Contains(err.Error(), "only for pipelines under .plumbix/pipelines/") {
		t.Errorf("source in the change pipeline: %v", err)
	}
}
