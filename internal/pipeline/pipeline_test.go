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
		{"run not string", wrap("  - {name: a, image: x, run: {sh: x}}\n"), "steps[0].run", 3, "string"},
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

func TestWithoutValidators(t *testing.T) {
	p, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	q := p.WithoutValidators()
	if len(q.Steps) != 2 || !reflect.DeepEqual(q.Order(), []string{"test", "lint"}) {
		t.Errorf("steps %d order %v", len(q.Steps), q.Order())
	}
	if len(p.Steps) != 4 {
		t.Error("original pipeline was modified")
	}
}

const extended = `version: 1
steps:
  - name: build
    image: golang:1.27
    run:
      - go build ./...
      - go vet ./...
  - name: test
    image: golang:1.27
    run: go test ./...
    needs: [build]
    retries: 2
  - name: contract
    image: golang:1.27
    run: go run ./cmd/openapi-diff
    needs: [build]
    allow_failure: true
    when:
      paths: ["api/**", "internal/http/*.go"]
  - name: validate
    image: plumbix-reference-validator:dev
    validator: true
    needs: [test, contract]
    retries: 1
stages:
  checks: [test, contract]
`

func TestParseExtendedFields(t *testing.T) {
	p, err := Parse([]byte(extended))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	build, test, contract, validate := p.Steps[0], p.Steps[1], p.Steps[2], p.Steps[3]
	if want := []string{"go build ./...", "go vet ./..."}; !reflect.DeepEqual(build.Commands, want) {
		t.Errorf("build commands = %q", build.Commands)
	}
	if build.Run != "set -e\ngo build ./...\ngo vet ./..." {
		t.Errorf("build script = %q", build.Run)
	}
	if test.Commands != nil || test.Run != "go test ./..." {
		t.Errorf("string run = %+v", test)
	}
	if test.Retries != 2 || build.Retries != 0 || validate.Retries != 1 {
		t.Errorf("retries = %d %d %d", test.Retries, build.Retries, validate.Retries)
	}
	if !contract.AllowFailure || test.AllowFailure {
		t.Errorf("allow_failure = %v %v", contract.AllowFailure, test.AllowFailure)
	}
	if want := []string{"api/**", "internal/http/*.go"}; !reflect.DeepEqual(contract.OnlyPaths, want) || test.OnlyPaths != nil {
		t.Errorf("when.paths = %q %q", contract.OnlyPaths, test.OnlyPaths)
	}
	if build.Stage != "" || test.Stage != "checks" || contract.Stage != "checks" || validate.Stage != "" {
		t.Errorf("stages = %q %q %q %q", build.Stage, test.Stage, contract.Stage, validate.Stage)
	}
	if want := []Stage{{Name: "checks", Steps: []string{"test", "contract"}}}; !reflect.DeepEqual(p.Stages, want) {
		t.Errorf("Stages = %+v", p.Stages)
	}
	if want := []string{"build", "test", "contract", "validate"}; !reflect.DeepEqual(p.Order(), want) {
		t.Errorf("order = %v", p.Order())
	}
}

func TestExtendedFieldErrors(t *testing.T) {
	cases := []struct {
		name, src, path string
		line            int
		msg             string
	}{
		{"run empty list", wrap("  - {name: a, image: x, run: []}\n"), "steps[0].run", 3, "at least one command"},
		{"run list item empty", wrap("  - {name: a, image: x, run: ['']}\n"), "steps[0].run[0]", 3, "must not be empty"},
		{"run list item not string", wrap("  - {name: a, image: x, run: [1]}\n"), "steps[0].run[0]", 3, "string"},
		{"retries text", wrap("  - {name: a, image: x, run: x, retries: two}\n"), "steps[0].retries", 3, "integer"},
		{"retries negative", wrap("  - {name: a, image: x, run: x, retries: -1}\n"), "steps[0].retries", 3, "between 0 and 5"},
		{"retries too many", wrap("  - {name: a, image: x, run: x, retries: 6}\n"), "steps[0].retries", 3, "between 0 and 5"},
		{"allow_failure text", wrap("  - {name: a, image: x, run: x, allow_failure: yes}\n"), "steps[0].allow_failure", 3, "boolean"},
		{"allow_failure on validator", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, allow_failure: true}\n"), "steps[1].allow_failure", 4, "validator"},
		{"when not mapping", wrap("  - {name: a, image: x, run: x, when: always}\n"), "steps[0].when", 3, "mapping"},
		{"when unknown key", wrap("  - {name: a, image: x, run: x, when: {branch: main}}\n"), "steps[0].when.branch", 3, "unknown field"},
		{"when paths empty", wrap("  - {name: a, image: x, run: x, when: {paths: []}}\n"), "steps[0].when.paths", 3, "at least one"},
		{"when path absolute", wrap("  - {name: a, image: x, run: x, when: {paths: [/etc/*]}}\n"), "steps[0].when.paths[0]", 3, "relative"},
		{"when path bad glob", wrap("  - {name: a, image: x, run: x, when: {paths: ['a/[']}}\n"), "steps[0].when.paths[0]", 3, "glob"},
		{"when on validator", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, when: {paths: [a]}}\n"), "steps[1].when", 4, "validator"},
		{"stages not mapping", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\nstages: [a]\n", "stages", 4, "mapping"},
		{"stage bad name", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\nstages:\n  Big Stage: [a]\n", "stages.Big Stage", 5, "lowercase"},
		{"stage unknown step", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\nstages:\n  s: [b]\n", "stages.s[0]", 5, "unknown step"},
		{"stage step twice", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\nstages:\n  s: [a]\n  t: [a]\n", "stages.t[0]", 6, "already in stage"},
		{"stage with validator", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true}\nstages:\n  s: [v]\n", "stages.s[0]", 6, "validator"},
		{"stage empty", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\nstages:\n  s: []\n", "stages.s", 5, "at least one"},
		{"external text", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: yes}\n"), "steps[1].external", 4, "boolean"},
		{"external on plain step", wrap("  - {name: a, external: true}\n"), "steps[0].external", 3, "validator"},
		{"external with image", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, external: true}\n"), "steps[1].image", 4, "external"},
		{"external with run", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, run: x}\n"), "steps[1].run", 4, "external"},
		{"external with secrets", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, secrets: [K]}\n"), "steps[1].secrets", 4, "external"},
		{"external with env", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, env: {A: b}}\n"), "steps[1].env", 4, "external"},
		{"external with retries", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, retries: 1}\n"), "steps[1].retries", 4, "external"},
		{"reviewer on plain step", wrap("  - {name: a, image: x, run: x, reviewer: grok}\n"), "steps[0].reviewer", 3, "validator"},
		{"advisory on plain step", wrap("  - {name: a, image: x, run: x, advisory: true}\n"), "steps[0].advisory", 3, "validator"},
		{"effort on plain step", wrap("  - {name: a, image: x, run: x, effort: low}\n"), "steps[0].effort", 3, "validator"},
		{"effort value", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, effort: max}\n"), "steps[1].effort", 4, "low, medium or high"},
		{"advisory text", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, advisory: yes}\n"), "steps[1].advisory", 4, "boolean"},
		{"cache relative", wrap("  - {name: a, image: x, run: x, cache: [go/pkg/mod]}\n"), "steps[0].cache[0]", 3, "absolute"},
		{"cache dotdot", wrap("  - {name: a, image: x, run: x, cache: [/root/../etc]}\n"), "steps[0].cache[0]", 3, ".."},
		{"cache root", wrap("  - {name: a, image: x, run: x, cache: [/]}\n"), "steps[0].cache[0]", 3, "root"},
		{"cache workspace", wrap("  - {name: a, image: x, run: x, cache: [/workspace/node_modules]}\n"), "steps[0].cache[0]", 3, "workspace"},
		{"cache duplicate", wrap("  - {name: a, image: x, run: x, cache: [/go/pkg/mod, /go/pkg/mod/]}\n"), "steps[0].cache[1]", 3, "twice"},
		{"cache on validator", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, cache: [/go/pkg/mod]}\n"), "steps[1].cache", 4, "validator"},
		{"cache on external", wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, cache: [/go/pkg/mod]}\n"), "steps[1].cache", 4, "validator"},
		{"cache too many", wrap("  - {name: a, image: x, run: x, cache: [/c1, /c2, /c3, /c4, /c5, /c6, /c7, /c8, /c9]}\n"), "steps[0].cache", 3, "at most 8"},
		{"reviewer shape", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, reviewer: Grok 4}\n"), "steps[1].reviewer", 4, "lowercase"},
		{"reviewer not text", wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, reviewer: [grok]}\n"), "steps[1].reviewer", 4, "string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			for _, e := range ve.Errors {
				if e.Path == c.path && e.Line == c.line && strings.Contains(e.Message, c.msg) {
					return
				}
			}
			t.Errorf("no error at %s line %d containing %q; got %v", c.path, c.line, c.msg, err)
		})
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"api/**", "api/openapi.yaml", true},
		{"api/**", "api/v1/spec.yaml", true},
		{"api/**", "api", false},
		{"api/**", "apix/spec.yaml", false},
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/http/server.go", true},
		{"**/*.go", "internal/http/server.ts", false},
		{"internal/http/*.go", "internal/http/server.go", true},
		{"internal/http/*.go", "internal/http/deep/server.go", false},
		{"docs/*.md", "docs/a.md", true},
		{"README.md", "README.md", true},
		{"README.md", "docs/README.md", false},
		{"web/**/*.tsx", "web/src/pages/ChangePage.tsx", true},
		{"web/**/*.tsx", "web/App.tsx", true},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestExternalValidatorNeedsNoImage(t *testing.T) {
	p, err := Parse([]byte(wrap("  - {name: a, image: x, run: x}\n  - {name: v, validator: true, external: true, needs: [a], timeout: 2h}\n")))
	if err != nil {
		t.Fatal(err)
	}
	v := p.Steps[1]
	if !v.External || !v.Validator || v.Image != "" || v.Timeout != 2*time.Hour {
		t.Errorf("external step = %+v", v)
	}
	if p.Steps[0].External {
		t.Error("a plain step reads as external")
	}
}

// TestReviewerBindsAValidatorStepToAProfile (WU-028): reviewer names the
// profile that judges, on container and external validators alike.
func TestReviewerBindsAValidatorStepToAProfile(t *testing.T) {
	p, err := Parse([]byte(wrap("  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true, reviewer: grok-4, advisory: true, effort: low}\n  - {name: e, validator: true, external: true, reviewer: claude}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[1].Reviewer != "grok-4" || p.Steps[2].Reviewer != "claude" || p.Steps[0].Reviewer != "" {
		t.Errorf("reviewers = %q %q %q", p.Steps[0].Reviewer, p.Steps[1].Reviewer, p.Steps[2].Reviewer)
	}
	if !p.Steps[1].Advisory || p.Steps[2].Advisory {
		t.Errorf("advisory = %v %v, want only the grok step", p.Steps[1].Advisory, p.Steps[2].Advisory)
	}
	if p.Steps[1].Effort != "low" || p.Steps[2].Effort != "" {
		t.Errorf("effort = %q %q", p.Steps[1].Effort, p.Steps[2].Effort)
	}
}

// TestCacheListsContainerPathsKeptBetweenRuns (WU-029): cache names absolute
// container paths, cleaned, on plain steps.
func TestCacheListsContainerPathsKeptBetweenRuns(t *testing.T) {
	p, err := Parse([]byte(wrap("  - {name: a, image: x, run: x, cache: [/go/pkg/mod, \"/root/.cache/go-build/\"]}\n  - {name: b, image: x, run: x}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Steps[0].Cache; len(got) != 2 || got[0] != "/go/pkg/mod" || got[1] != "/root/.cache/go-build" {
		t.Errorf("cache = %q", got)
	}
	if p.Steps[1].Cache != nil {
		t.Errorf("a step without cache has %q", p.Steps[1].Cache)
	}
}
