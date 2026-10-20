package azure

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/importer"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// convertWith converts the file root of files, with repos as the other
// repositories, and checks the result parses.
func convertWith(t *testing.T, root string, files map[string]string, repos map[string]importer.Tree) (importer.Converted, *pipeline.Pipeline) {
	t.Helper()
	look := importer.Lookup{Files: map[string][]byte{}}
	for p, s := range files {
		look.Files[p] = []byte(s)
	}
	if repos != nil {
		look.Repos = func(r importer.RepoRef) (importer.Tree, error) {
			tree, ok := repos[r.Kind+":"+r.Name+"@"+r.Ref]
			if !ok {
				return importer.Tree{}, errors.New("repository " + r.Name + " is not registered in Plumbix")
			}
			return tree, nil
		}
	}
	c, err := Source{}.Convert(root, look.Files[root], look)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.ParseTriggered(c.Content)
	if err != nil {
		t.Fatalf("converted %s does not parse: %v\n%s", root, err, c.Content)
	}
	return c, p
}

func tree(files map[string]string, label string) importer.Tree {
	t := importer.Tree{Files: map[string][]byte{}, Label: label}
	for p, s := range files {
		t.Files[p] = []byte(s)
	}
	return t
}

// TestStageTemplate (WU-047): a stage template relative to the pipeline's
// folder, with each over a list parameter, if/else in steps and a boolean
// default; steps name the template as their source.
func TestStageTemplate(t *testing.T) {
	_, p := convertWith(t, "pipelines/azure-pipelines.yml", map[string]string{
		"pipelines/azure-pipelines.yml": `trigger: [main]
stages:
  - template: templates/deploy.yml
    parameters:
      service: orders
      envs: [dev, prod]
`,
		"pipelines/templates/deploy.yml": `parameters:
  - name: service
    type: string
  - name: envs
    type: object
    default: [dev]
  - name: push
    type: boolean
    default: false
stages:
  - ${{ each e in parameters.envs }}:
    - stage: deploy_${{ e }}
      jobs:
        - job: build
          container: node:20
          steps:
            - script: echo ${{ parameters.service }} ${{ e }}
            - ${{ if parameters.push }}:
              - script: docker push
            - ${{ else }}:
              - script: echo no push
`,
	}, nil)
	if p.Off != nil {
		t.Errorf("pipeline disabled: %+v", p.Off)
	}
	for _, env := range []string{"dev", "prod"} {
		s := step(t, p, "deploy-"+env+"-build")
		if reason(s) != "" || s.Image != "node:20" || !slices.Equal(s.Commands, []string{"echo orders " + env, "echo no push"}) {
			t.Errorf("%s = %+v", env, s)
		}
		if s.Source == nil || s.Source.File != "pipelines/templates/deploy.yml" || s.Source.Line == 0 {
			t.Errorf("%s source = %+v", env, s.Source)
		}
	}
	if len(p.Stages) != 2 || p.Stages[0].Name != "deploy-dev" {
		t.Errorf("stages = %+v", p.Stages)
	}
	if dev, prod := step(t, p, "deploy-dev-build"), step(t, p, "deploy-prod-build"); dev.Source.Hash == prod.Source.Hash {
		t.Error("the hash should cover the job after expansion")
	}
}

// TestJobAndStepTemplates (WU-047): a job template from the root, a nested
// step template relative to it, and a step list passed as a parameter and
// spliced in; a variable template's variables reach the job.
func TestJobAndStepTemplates(t *testing.T) {
	_, p := convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": `trigger: [main]
variables:
  - template: ci/vars.yml
jobs:
  - template: /ci/jobs.yml
    parameters:
      steps:
        - script: npm test -- $(REGION)
`,
		"ci/vars.yml": "variables:\n  - name: REGION\n    value: eu\n",
		"ci/jobs.yml": `parameters:
  - name: steps
    type: stepList
    default: []
jobs:
  - job: test
    container: node:20
    steps:
      - template: steps/install.yml
      - ${{ parameters.steps }}
`,
		"ci/steps/install.yml": "steps:\n  - script: npm ci\n",
	}, nil)
	s := step(t, p, "test")
	if reason(s) != "" || !slices.Equal(s.Commands, []string{"npm ci", "npm test -- ${REGION}"}) || s.Env["REGION"] != "eu" {
		t.Errorf("test = %+v", s)
	}
	if s.Source.File != "ci/jobs.yml" {
		t.Errorf("source = %+v", s.Source)
	}
}

// TestExtends (WU-047): extends takes the template's jobs; the root keeps
// its triggers.
func TestExtends(t *testing.T) {
	_, p := convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": "trigger: [main]\nextends:\n  template: base.yml\n  parameters:\n    image: node:20\n",
		"base.yml":            "parameters:\n  - name: image\njobs:\n  - job: a\n    container: ${{ parameters.image }}\n    steps:\n      - script: make\n",
	}, nil)
	if !slices.Equal(p.On.Push.Branches, []string{"main"}) {
		t.Errorf("on = %+v", p.On.Push)
	}
	if s := step(t, p, "a"); s.Image != "node:20" || reason(s) != "" || s.Run == "" {
		t.Errorf("a = %+v", s)
	}
}

// TestTemplateFailures (WU-047): a template that cannot be read or
// expanded stays one disabled step, reason template, saying why.
func TestTemplateFailures(t *testing.T) {
	files := map[string]string{
		"azure-pipelines.yml": `trigger: none
stages:
  - template: missing.yml
  - template: needs.yml
  - template: needs.yml
    parameters: {name: a, extra: b}
  - template: choice.yml
    parameters: {size: huge}
  - template: runtime.yml
  - template: self.yml
  - template: ../outside.yml
  - stage: build
    jobs:
      - job: lint
        container: node:20
        steps:
          - template: missing-steps.yml
          - script: npm run lint
`,
		"needs.yml":   "parameters:\n  - name: name\nstages:\n  - stage: s\n    jobs:\n      - job: j\n        container: x\n        steps:\n          - script: echo ${{ parameters.name }}\n",
		"choice.yml":  "parameters:\n  - name: size\n    values: [small, large]\nstages:\n  - stage: c\n    jobs:\n      - job: j\n        container: x\n        steps:\n          - script: x\n",
		"runtime.yml": "stages:\n  - stage: r\n    jobs:\n      - job: j\n        container: x\n        steps:\n          - script: echo ${{ variables['Build.SourceBranch'] }}\n",
		"self.yml":    "stages:\n  - template: self.yml\n",
	}
	c, p := convertWith(t, "azure-pipelines.yml", files, nil)
	want := []string{"missing.yml", "name", "extra", "huge", "Build.SourceBranch", "deep", "leaves the repository"}
	var off []importer.Step
	for _, s := range c.Steps {
		if s.Reason == "template" {
			off = append(off, s)
		}
	}
	if len(off) != len(want)+1 {
		t.Fatalf("disabled template steps = %+v", c.Steps)
	}
	for i, w := range want {
		if !strings.Contains(off[i].Note, w) {
			t.Errorf("step %d note %q misses %q", i, off[i].Note, w)
		}
	}
	lint := step(t, p, "build-lint")
	if reason(lint) != "template" || !strings.Contains(lint.Disabled.Note, "missing-steps.yml") {
		t.Errorf("lint = %+v", lint)
	}
}

// TestRootParameters (WU-047): run-time parameters with defaults expand
// with them and are noted; one without a default still disables the
// pipeline with reason trigger.
func TestRootParameters(t *testing.T) {
	c, p := convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": `trigger: [main]
parameters:
  - name: target
    default: staging
  - name: dryRun
    type: boolean
    default: true
jobs:
  - job: deploy
    container: alpine:3
    steps:
      - script: echo ${{ parameters.target }} ${{ parameters.dryRun }}
      - script: ${{ parameters.dryRun }}
`,
	}, nil)
	if p.Off != nil {
		t.Errorf("disabled: %+v", p.Off)
	}
	if s := step(t, p, "deploy"); !slices.Equal(s.Commands, []string{"echo staging True", "True"}) {
		t.Errorf("deploy = %+v", s)
	}
	if notes := strings.Join(c.Notes, "\n"); !strings.Contains(notes, "target") || !strings.Contains(notes, "default") {
		t.Errorf("notes = %s", notes)
	}

	_, p = convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": "trigger: [main]\nparameters:\n  - name: target\njobs:\n  - job: deploy\n    container: alpine:3\n    steps:\n      - script: echo ${{ parameters.target }}\n",
	}, nil)
	if p.Off == nil || p.Off.Reason != "trigger" || !strings.Contains(p.Off.Note, "target") {
		t.Errorf("off = %+v", p.Off)
	}
}

// TestRepositoryTemplates (WU-047): path@alias reads the repository the
// root's resources name; references inside it stay there unless @self;
// the note names the repository's label; an unknown one disables.
func TestRepositoryTemplates(t *testing.T) {
	files := map[string]string{
		"azure-pipelines.yml": `trigger: [main]
resources:
  repositories:
    - repository: infra
      type: git
      name: Shop/infra
      ref: refs/heads/main
    - repository: other
      type: github
      name: someone/else
stages:
  - template: stages/build.yml@infra
    parameters: {service: orders}
  - template: stages/x.yml@other
  - template: stages/x.yml@nowhere
`,
		"common/steps.yml": "steps:\n  - script: make lint\n",
	}
	repos := map[string]importer.Tree{
		"git:Shop/infra@refs/heads/main": tree(map[string]string{
			"stages/build.yml": "parameters:\n  - name: service\nstages:\n  - stage: build\n    jobs:\n      - template: ../jobs/build.yml\n        parameters: {service: '${{ parameters.service }}'}\n",
			"jobs/build.yml":   "parameters:\n  - name: service\njobs:\n  - job: image\n    container: golang:1.24\n    steps:\n      - template: /common/steps.yml@self\n      - script: go build ./${{ parameters.service }}\n",
		}, "shop-infra at 1a2b3c4"),
	}
	c, p := convertWith(t, "azure-pipelines.yml", files, repos)
	s := step(t, p, "build-image")
	if reason(s) != "" || !slices.Equal(s.Commands, []string{"make lint", "go build ./orders"}) || s.Source.File != "jobs/build.yml@infra" {
		t.Errorf("image = %+v source %+v", s, s.Source)
	}
	notes := strings.Join(c.Notes, "\n")
	if !strings.Contains(notes, "shop-infra at 1a2b3c4") {
		t.Errorf("notes = %s", notes)
	}
	var off []string
	for _, st := range c.Steps {
		if st.Reason == "template" {
			off = append(off, st.Note)
		}
	}
	if len(off) != 2 || !strings.Contains(off[0], "not registered") || !strings.Contains(off[1], "nowhere") {
		t.Errorf("disabled = %q", off)
	}

	c, _ = convertWith(t, "azure-pipelines.yml", files, nil)
	if c.Steps[0].Reason != "template" || !strings.Contains(c.Steps[0].Note, "other repositories") {
		t.Errorf("without repos: %+v", c.Steps[0])
	}
}

// TestDirectivesInMappings (WU-047): an if, elseif, else chain picks keys
// of a mapping; ${{ insert }} is not supported, so the root is converted
// as written, with a note.
func TestDirectivesInMappings(t *testing.T) {
	src := `trigger: [main]
parameters:
  - name: os
    default: mac
jobs:
  - job: a
    ${{ if eq(parameters.os, 'linux') }}:
      container: alpine:3
    ${{ elseif eq(parameters.os, 'mac') }}:
      container: mac:1
    ${{ else }}:
      container: other:1
    steps:
      - script: make
`
	_, p := convertWith(t, "azure-pipelines.yml", map[string]string{"azure-pipelines.yml": src}, nil)
	if s := step(t, p, "a"); s.Image != "mac:1" || reason(s) != "" {
		t.Errorf("a = %+v", s)
	}

	src = strings.Replace(src, "    steps:", "    ${{ insert }}: {}\n    steps:", 1)
	c, p := convertWith(t, "azure-pipelines.yml", map[string]string{"azure-pipelines.yml": src}, nil)
	if !strings.Contains(strings.Join(c.Notes, "\n"), "insert") || reason(step(t, p, "a")) != "template" {
		t.Errorf("insert: notes %v steps %+v", c.Notes, c.Steps)
	}
}

// TestExpansionLimits (WU-047): more than 100 template files fail the
// reference past the limit; a root that expands past 100,000 nodes is
// converted as written.
func TestExpansionLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("trigger: none\nstages:\n")
	for range 101 {
		b.WriteString("  - template: s.yml\n")
	}
	c, _ := convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": b.String(),
		"s.yml":               "stages:\n  - stage: s\n    jobs:\n      - job: j\n        container: x\n        steps:\n          - script: x\n",
	}, nil)
	last := c.Steps[len(c.Steps)-1]
	if len(c.Steps) != 101 || last.Reason != "template" || !strings.Contains(last.Note, "100 template files") || c.Steps[99].Reason != "" {
		t.Errorf("last = %+v of %d", last, len(c.Steps))
	}

	list := "[" + strings.Repeat("x, ", 399) + "x]"
	c, _ = convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": "trigger: none\nparameters:\n  - name: a\n    type: object\n    default: " + list + `
jobs:
  - job: j
    container: x
    steps:
      - ${{ each i in parameters.a }}:
        - ${{ each k in parameters.a }}:
          - script: echo
`,
	}, nil)
	if !strings.Contains(strings.Join(c.Notes, "\n"), "100000 nodes") {
		t.Errorf("notes = %v", c.Notes)
	}
}

// TestRuntimeVariable (WU-047): a stage variable set by a $[ ] runtime
// expression is not a known value; a step using it is disabled.
func TestRuntimeVariable(t *testing.T) {
	_, p := convertWith(t, "azure-pipelines.yml", map[string]string{
		"azure-pipelines.yml": "trigger: [main]\nstages:\n  - stage: b\n    variables:\n      tag: $[replace('a', 'a', 'b')]\n    jobs:\n      - job: j\n        container: x\n        steps:\n          - script: echo $(tag)\n",
	}, nil)
	s := step(t, p, "b-j")
	if reason(s) != "variables" || !strings.Contains(s.Disabled.Note, "$(tag)") || s.Env["tag"] != "" {
		t.Errorf("step = %+v", s)
	}
}
