package pipeline

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const example = `version: 1
steps:
  - name: test
    image: golang:1.27
    run: go test ./...
    timeout: 10m
    artifacts: [coverage.out]
  - name: lint
    image: golangci/golangci-lint:v2.5.0
    run: golangci-lint run
    env:
      GOFLAGS: "-mod=readonly"
  - name: validate
    image: ghcr.io/eduard-alloy/plumbix-reference-validator:v0
    validator: true
    needs: [test, lint]
  - name: ai-review
    image: ghcr.io/eduard-alloy/plumbix-ai-validator:v0
    validator: true
    needs: [test, lint]
    secrets: [ANTHROPIC_API_KEY]
`

func TestParseExample(t *testing.T) {
	p, err := Parse([]byte(example))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Version != 1 || len(p.Steps) != 4 {
		t.Fatalf("version %d, %d steps; want 1 and 4", p.Version, len(p.Steps))
	}
	test := p.Steps[0]
	if test.Name != "test" || test.Image != "golang:1.27" || test.Run != "go test ./..." {
		t.Errorf("test step = %+v", test)
	}
	if test.Timeout != 10*time.Minute || !reflect.DeepEqual(test.Artifacts, []string{"coverage.out"}) {
		t.Errorf("test timeout/artifacts = %v %v", test.Timeout, test.Artifacts)
	}
	if p.Steps[1].Env["GOFLAGS"] != "-mod=readonly" {
		t.Errorf("lint env = %v", p.Steps[1].Env)
	}
	ai := p.Steps[3]
	if !ai.Validator || ai.Run != "" || !reflect.DeepEqual(ai.Needs, []string{"test", "lint"}) || !reflect.DeepEqual(ai.Secrets, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("ai-review step = %+v", ai)
	}
	if want := []string{"test", "lint", "validate", "ai-review"}; !reflect.DeepEqual(p.Order(), want) {
		t.Errorf("order = %v, want %v", p.Order(), want)
	}
}

func TestOrderPutsNeedsFirst(t *testing.T) {
	src := `version: 1
steps:
  - {name: c, image: x, run: x, needs: [b]}
  - {name: b, image: x, run: x, needs: [a]}
  - {name: a, image: x, run: x}
  - {name: d, image: x, run: x}
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "d", "b", "c"}; !reflect.DeepEqual(p.Order(), want) {
		t.Errorf("order = %v, want %v", p.Order(), want)
	}
}

// wrap builds a pipeline with the given steps block.
func wrap(steps string) string { return "version: 1\nsteps:\n" + steps }

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, src, path string
		line            int
		msg             string
	}{
		{"not yaml", "version: [1\n", "", 0, "yaml"},
		{"not a mapping", "- 1\n", "", 1, "mapping"},
		{"missing version", "steps:\n  - {name: a, image: x, run: x}\n", "version", 1, "required"},
		{"wrong version", "version: 2\nsteps:\n  - {name: a, image: x, run: x}\n", "version", 1, "must be 1"},
		{"version as string", "version: \"1\"\nsteps:\n  - {name: a, image: x, run: x}\n", "version", 1, "integer"},
		{"missing steps", "version: 1\n", "steps", 1, "required"},
		{"empty steps", "version: 1\nsteps: []\n", "steps", 2, "at least one"},
		{"unknown top field", "version: 1\non: push\nsteps:\n  - {name: a, image: x, run: x}\n", "on", 2, "unknown field"},
		{"unknown step field", wrap("  - name: a\n    image: x\n    run: x\n    matrix: [1]\n"), "steps[0].matrix", 6, "unknown field"},
		{"step not mapping", wrap("  - a\n"), "steps[0]", 3, "mapping"},
		{"missing name", wrap("  - {image: x, run: x}\n"), "steps[0].name", 3, "required"},
		{"bad name", wrap("  - {name: Build_It, image: x, run: x}\n"), "steps[0].name", 3, "lowercase"},
		{"name trailing dash", wrap("  - {name: a-, image: x, run: x}\n"), "steps[0].name", 3, "lowercase"},
		{"missing image", wrap("  - {name: a, run: x}\n"), "steps[0].image", 3, "required"},
		{"image with space", wrap("  - {name: a, image: 'go lang', run: x}\n"), "steps[0].image", 3, "whitespace"},
		{"missing run", wrap("  - {name: a, image: x}\n"), "steps[0].run", 3, "required"},
		{"empty run", wrap("  - {name: a, image: x, run: ''}\n"), "steps[0].run", 3, "required"},
		{"run not string", wrap("  - {name: a, image: x, run: [x]}\n"), "steps[0].run", 3, "string"},
		{"validator not bool", wrap("  - {name: a, image: x, validator: yes-please}\n"), "steps[0].validator", 3, "boolean"},
		{"env value not string", wrap("  - name: a\n    image: x\n    run: x\n    env:\n      PORT: 8080\n"), "steps[0].env.PORT", 7, "string"},
		{"env bad key", wrap("  - name: a\n    image: x\n    run: x\n    env:\n      1BAD: v\n"), "steps[0].env.1BAD", 7, "name"},
		{"env reserved key", wrap("  - name: a\n    image: x\n    run: x\n    env:\n      PLUMBIX_X: v\n"), "steps[0].env.PLUMBIX_X", 7, "reserved"},
		{"secret bad name", wrap("  - {name: a, image: x, run: x, secrets: [bad-name]}\n"), "steps[0].secrets[0]", 3, "name"},
		{"secret duplicate", wrap("  - {name: a, image: x, run: x, secrets: [K, K]}\n"), "steps[0].secrets[1]", 3, "duplicate"},
		{"secret also env", wrap("  - name: a\n    image: x\n    run: x\n    env: {K: v}\n    secrets: [K]\n"), "steps[0].secrets[0]", 7, "env"},
		{"artifact absolute", wrap("  - {name: a, image: x, run: x, artifacts: [/etc/passwd]}\n"), "steps[0].artifacts[0]", 3, "relative"},
		{"artifact dotdot", wrap("  - {name: a, image: x, run: x, artifacts: [out/../../x]}\n"), "steps[0].artifacts[0]", 3, ".."},
		{"timeout invalid", wrap("  - {name: a, image: x, run: x, timeout: soon}\n"), "steps[0].timeout", 3, "duration"},
		{"timeout zero", wrap("  - {name: a, image: x, run: x, timeout: 0s}\n"), "steps[0].timeout", 3, "above zero"},
		{"timeout too long", wrap("  - {name: a, image: x, run: x, timeout: 25h}\n"), "steps[0].timeout", 3, "24h"},
		{"duplicate name", wrap("  - {name: a, image: x, run: x}\n  - {name: a, image: x, run: x}\n"), "steps[1].name", 4, "duplicate"},
		{"needs unknown", wrap("  - {name: a, image: x, run: x, needs: [b]}\n"), "steps[0].needs[0]", 3, "unknown step"},
		{"needs self", wrap("  - {name: a, image: x, run: x, needs: [a]}\n"), "steps[0].needs[0]", 3, "itself"},
		{"needs duplicate", wrap("  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x, needs: [a, a]}\n"), "steps[1].needs[1]", 4, "duplicate"},
		{"needs validator", wrap("  - {name: v, image: x, validator: true}\n  - {name: b, image: x, run: x, needs: [v]}\n"), "steps[1].needs[0]", 4, "validator"},
		{"cycle", wrap("  - {name: a, image: x, run: x, needs: [b]}\n  - {name: b, image: x, run: x, needs: [a]}\n"), "steps", 2, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
			for _, e := range verr.Errors {
				if e.Path == tc.path && e.Line == tc.line && strings.Contains(e.Message, tc.msg) {
					return
				}
			}
			t.Fatalf("no error with path %q, line %d, message containing %q; got %+v", tc.path, tc.line, tc.msg, verr.Errors)
		})
	}
}

func TestReportsAllErrors(t *testing.T) {
	src := wrap("  - {name: Bad, image: x}\n  - {name: b, run: x, needs: [zz]}\n")
	_, err := Parse([]byte(src))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if len(verr.Errors) < 4 {
		t.Fatalf("got %d errors, want at least 4 (bad name, missing run, missing image, unknown need): %+v", len(verr.Errors), verr.Errors)
	}
	if !strings.Contains(err.Error(), "steps[0].name") {
		t.Errorf("Error() = %q, want it to list paths", err.Error())
	}
}
