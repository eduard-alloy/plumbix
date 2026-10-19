package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestDisabledStepNeedsNoImageOrRun (WU-044): an imported step Plumbix
// cannot run stays in the file with its reason, image and run optional.
func TestDisabledStepNeedsNoImageOrRun(t *testing.T) {
	p, err := Parse([]byte(wrap("  - {name: a, image: x, run: x}\n  - {name: b, needs: [a], disabled: {reason: task, note: PublishTestResults@2}}\n  - {name: c, image: x, run: docker build ., disabled: {reason: docker}}\n")))
	if err != nil {
		t.Fatal(err)
	}
	b := p.Steps[1]
	if b.Disabled == nil || b.Disabled.Reason != "task" || b.Disabled.Note != "PublishTestResults@2" || b.Image != "" || b.Run != "" {
		t.Errorf("disabled step = %+v", b)
	}
	if p.Steps[0].Disabled != nil {
		t.Error("an enabled step reads as disabled")
	}
	if !p.Disabled() {
		t.Error("a pipeline with disabled steps is not disabled")
	}
	if got, want := p.Blockers(), map[string]int{"task": 1, "docker": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("blockers = %v, want %v", got, want)
	}
}

func TestEnabledPipelineHasNoBlockers(t *testing.T) {
	p, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if p.Disabled() || len(p.Blockers()) != 0 {
		t.Errorf("disabled = %v, blockers = %v", p.Disabled(), p.Blockers())
	}
}

func TestDisabledErrors(t *testing.T) {
	cases := map[string]struct{ step, path, msg string }{
		"unknown reason": {"{name: a, image: x, run: x, disabled: {reason: broken}}", "steps[0].disabled.reason", "must be one of"},
		"no reason":      {"{name: a, image: x, run: x, disabled: {note: why}}", "steps[0].disabled.reason", "is required"},
		"unknown key":    {"{name: a, image: x, run: x, disabled: {reason: user, by: me}}", "steps[0].disabled.by", "unknown field"},
		"not a map":      {"{name: a, image: x, run: x, disabled: true}", "steps[0].disabled", "must be a mapping"},
		"long note":      {"{name: a, image: x, run: x, disabled: {reason: user, note: " + strings.Repeat("n", 501) + "}}", "steps[0].disabled.note", "at most 500"},
		"unknown need":   {"{name: a, needs: [nope], disabled: {reason: task}}", "steps[0].needs[0]", "unknown step"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(wrap("  - " + c.step + "\n")))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v", err)
			}
			for _, e := range ve.Errors {
				if e.Path == c.path && strings.Contains(e.Message, c.msg) {
					return
				}
			}
			t.Errorf("no %s error %q in %v", c.path, c.msg, ve.Errors)
		})
	}
}

// TestReasonsMatchTheSchema keeps the published reason list and the
// parser's equal.
func TestReasonsMatchTheSchema(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs struct {
			Step struct {
				Properties struct {
					Disabled struct {
						Properties struct {
							Reason struct {
								Enum []string `json:"enum"`
							} `json:"reason"`
						} `json:"properties"`
					} `json:"disabled"`
				} `json:"properties"`
			} `json:"step"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range Reasons {
		codes = append(codes, r.Code)
		if r.Description == "" {
			t.Errorf("reason %s has no description", r.Code)
		}
	}
	if got := s.Defs.Step.Properties.Disabled.Properties.Reason.Enum; !slices.Equal(got, codes) {
		t.Errorf("schema reasons %v, parser reasons %v", got, codes)
	}
}

// TestReasonsMatchTheOpenAPI keeps the API's copy of the list equal too.
func TestReasonsMatchTheOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Properties map[string]struct {
						Enum []string `yaml:"enum"`
					} `yaml:"properties"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range Reasons {
		codes = append(codes, r.Code)
	}
	if got := doc.Components.Schemas["PipelineStep"].Properties["disabled"].Properties["reason"].Enum; !slices.Equal(got, codes) {
		t.Errorf("openapi reasons %v, parser reasons %v", got, codes)
	}
}
