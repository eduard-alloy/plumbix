package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestParseTriggered (WU-045): a file under .plumbix/pipelines/ says what
// starts it, and may be disabled as a whole.
func TestParseTriggered(t *testing.T) {
	src := "version: 1\non:\n  push:\n    branches: [main, \"release/*\"]\n    paths: [\"svc/**\"]\n  manual: true\ndisabled: {reason: trigger, note: \"tags: v*\"}\nsteps:\n  - {name: a, image: x, run: x}\n"
	p, err := ParseTriggered([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := &Triggers{Push: &Push{Branches: []string{"main", "release/*"}, Paths: []string{"svc/**"}}, Manual: true}
	if !reflect.DeepEqual(p.On, want) {
		t.Errorf("on = %+v, want %+v", p.On, want)
	}
	if p.Off == nil || p.Off.Reason != "trigger" || p.Off.Note != "tags: v*" {
		t.Errorf("pipeline disabled = %+v", p.Off)
	}
	if !p.Disabled() || !reflect.DeepEqual(p.Blockers(), map[string]int{"trigger": 1}) {
		t.Errorf("disabled = %v, blockers = %v", p.Disabled(), p.Blockers())
	}
}

func TestTriggeredErrors(t *testing.T) {
	cases := map[string]struct {
		src, path, msg string
		change         bool
	}{
		"no on":             {src: "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on", msg: "is required"},
		"empty on":          {src: "version: 1\non: {}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on", msg: "push or manual"},
		"no branches":       {src: "version: 1\non: {push: {paths: [a]}}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on.push.branches", msg: "is required"},
		"bad branch glob":   {src: "version: 1\non: {push: {branches: [\"[\"]}}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on.push.branches[0]", msg: "valid glob"},
		"unknown trigger":   {src: "version: 1\non: {schedule: x}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on.schedule", msg: "unknown field"},
		"manual not bool":   {src: "version: 1\non: {manual: yes please}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on.manual", msg: "boolean"},
		"validator":         {src: "version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n  - {name: v, image: x, validator: true}\n", path: "steps[1].validator", msg: ".plumbix/pipeline.yaml"},
		"on in the change":  {src: "version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "on", msg: ".plumbix/pipelines/", change: true},
		"off in the change": {src: "version: 1\ndisabled: {reason: user}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "disabled", msg: ".plumbix/pipelines/", change: true},
		"bad off reason":    {src: "version: 1\non: {manual: true}\ndisabled: {reason: later}\nsteps:\n  - {name: a, image: x, run: x}\n", path: "disabled.reason", msg: "must be one of"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parse := ParseTriggered
			if c.change {
				parse = Parse
			}
			_, err := parse([]byte(c.src))
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

func TestPushMatches(t *testing.T) {
	push := &Push{Branches: []string{"main", "release/*"}, Paths: []string{"svc/**"}}
	cases := []struct {
		branch  string
		changed []string
		isNew   bool
		want    bool
	}{
		{"main", []string{"svc/a.go"}, false, true},
		{"main", []string{"web/a.ts"}, false, false},
		{"release/1.2", []string{"svc/x/y.go"}, false, true},
		{"release/1.2/hotfix", []string{"svc/a.go"}, false, false},
		{"feature", []string{"svc/a.go"}, false, false},
		{"main", nil, true, true},
	}
	for _, c := range cases {
		if got := push.Matches(c.branch, c.changed, c.isNew); got != c.want {
			t.Errorf("Matches(%q, %v, new %v) = %v, want %v", c.branch, c.changed, c.isNew, got, c.want)
		}
	}
	if !(&Push{Branches: []string{"**"}}).Matches("any/branch", []string{"x"}, false) {
		t.Error("** should match any branch with no paths")
	}
}

// TestPipelineReasonsMatchTheSchema keeps the top-level disabled reasons
// and PipelineReasons equal.
func TestPipelineReasonsMatchTheSchema(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties struct {
			Disabled struct {
				Properties struct {
					Reason struct {
						Enum []string `json:"enum"`
					} `json:"reason"`
				} `json:"properties"`
			} `json:"disabled"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range PipelineReasons {
		codes = append(codes, r.Code)
	}
	if got := s.Properties.Disabled.Properties.Reason.Enum; !slices.Equal(got, codes) {
		t.Errorf("schema %v, parser %v", got, codes)
	}
}
