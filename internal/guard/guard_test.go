package guard

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

const deploy = `version: 1
on:
  push:
    branches: [main, "release/*"]
  manual: true
steps:
  - name: build
    image: docker:27
    run: docker build -t app .
    secrets: [REGISTRY_TOKEN]
  - name: push
    image: alpine:3
    run: 'curl -H "Authorization: Bearer $DEPLOY_TOKEN" https://deploy.example/hook'
    secrets: [DEPLOY_TOKEN, DEPLY_TOKEN]
`

func messages(rs []validation.Risk) string {
	var b strings.Builder
	for _, r := range rs {
		if r.Area != "pipelines" || !r.HumanRequired {
			b.WriteString("BAD RISK SHAPE: ")
		}
		b.WriteString(r.Message + "\n")
	}
	return b.String()
}

func has(t *testing.T, all string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(all, p) {
			t.Errorf("missing %q in:\n%s", p, all)
		}
	}
}

// TestAddedTriggeredPipeline (WU-051): the summary and every flag.
func TestAddedTriggeredPipeline(t *testing.T) {
	got := messages(Review(Input{
		Files:        []File{{Path: ".plumbix/pipelines/deploy.yaml", After: []byte(deploy)}},
		KnownSecrets: map[string]bool{"DEPLOY_TOKEN": true, "REGISTRY_TOKEN": true},
		Protected:    []string{"main"},
	}))
	has(t, got,
		".plumbix/pipelines/deploy.yaml added",
		"starts on push to main, release/*; by hand",
		"secrets DEPLOY_TOKEN (new), DEPLY_TOKEN (new), REGISTRY_TOKEN (new)",
		"images alpine:3, docker:27",
		"steps added: build, push",
		"secret DEPLY_TOKEN, named by step push in .plumbix/pipelines/deploy.yaml, is not set in the secrets file",
		"runs on branches that are not protected (release/*)",
		"step push uses a network tool with secret DEPLOY_TOKEN",
	)
	if strings.Contains(got, "BAD RISK SHAPE") {
		t.Error(got)
	}
	if strings.Contains(got, "step build uses a network tool") {
		t.Error("docker build with a secret it does not reference is not a network leak")
	}
}

func TestChangedAndDeletedPipelines(t *testing.T) {
	before := "version: 1\nsteps:\n  - {name: test, image: \"golang:1.26\", run: go test ./..., secrets: [OLD]}\n"
	after := "version: 1\nsteps:\n  - {name: test, image: \"golang:1.27\", run: go test -race ./..., secrets: [OLD]}\n  - {name: lint, image: \"golang:1.27\", run: go vet ./...}\n"
	got := messages(Review(Input{
		Files: []File{
			{Path: ".plumbix/pipeline.yaml", Before: []byte(before), After: []byte(after)},
			{Path: ".plumbix/pipelines/old.yaml", Before: []byte("version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n")},
			{Path: ".plumbix/README.md", After: []byte("notes")},
		},
		KnownSecrets: map[string]bool{"OLD": true},
		Protected:    []string{"main"},
	}))
	has(t, got,
		".plumbix/pipeline.yaml changed",
		"secrets OLD;",
		"images golang:1.27 (new)",
		"steps added: lint",
		"commands changed: test",
		".plumbix/pipelines/old.yaml deleted",
	)
	if strings.Contains(got, "README") || strings.Contains(got, "(new), OLD") {
		t.Errorf("unexpected:\n%s", got)
	}
}

func TestUnparsableAndUnchanged(t *testing.T) {
	got := messages(Review(Input{Files: []File{{Path: ".plumbix/pipelines/bad.yaml", After: []byte("version: 1\nsteps: []\n")}}}))
	has(t, got, ".plumbix/pipelines/bad.yaml does not parse")
	same := "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n"
	if rs := Review(Input{Files: []File{{Path: ".plumbix/pipeline.yaml", Before: []byte(same), After: []byte(same)}}}); len(rs) != 0 {
		t.Errorf("an unchanged file produced %v", rs)
	}
}

func TestIsPipelineFile(t *testing.T) {
	for path, want := range map[string]bool{
		".plumbix/pipeline.yaml":          true,
		".plumbix/pipelines/deploy.yaml":  true,
		".plumbix/pipelines/sub/x.yaml":   false,
		".plumbix/pipelines/notes.md":     false,
		".plumbix/README.md":              false,
		"services/.plumbix/pipeline.yaml": false,
	} {
		if got := IsPipelineFile(path); got != want {
			t.Errorf("IsPipelineFile(%q) = %v", path, got)
		}
	}
}

// TestOtherStepChangesAreNamed (WU-051 review): env, cache, needs and path
// changes are reported per step, never hidden behind a generic line.
func TestOtherStepChangesAreNamed(t *testing.T) {
	before := "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x, env: {URL: https://a}}\n  - {name: v, image: x, validator: true, needs: [a, b]}\n"
	after := "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n  - {name: b, image: x, run: x, needs: [a], env: {URL: https://b}, cache: [/c], when: {paths: [\"s/**\"]}}\n  - {name: v, image: x, validator: true, needs: [a, b], advisory: true, effort: low}\n"
	got := messages(Review(Input{Files: []File{{Path: ".plumbix/pipeline.yaml", Before: []byte(before), After: []byte(after)}}}))
	has(t, got, "env changed: b", "needs, caches or conditions changed: b, v")
	if strings.Contains(got, "formatting") {
		t.Errorf("a real change was summarised as formatting:\n%s", got)
	}
}
