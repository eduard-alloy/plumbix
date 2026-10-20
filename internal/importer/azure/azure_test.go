package azure

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/importer"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

func convert(t *testing.T, name string) (importer.Converted, *pipeline.Pipeline) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Source{}.Convert("pipelines/"+name, src, importer.Lookup{Files: map[string][]byte{"pipelines/" + name: src}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.ParseTriggered(c.Content)
	if err != nil {
		t.Fatalf("converted %s does not parse: %v\n%s", name, err, c.Content)
	}
	return c, p
}

func step(t *testing.T, p *pipeline.Pipeline, name string) pipeline.Step {
	t.Helper()
	for _, s := range p.Steps {
		if s.Name == name {
			return s
		}
	}
	var names []string
	for _, s := range p.Steps {
		names = append(names, s.Name)
	}
	t.Fatalf("no step %s in %v", name, names)
	return pipeline.Step{}
}

func reason(s pipeline.Step) string {
	if s.Disabled == nil {
		return ""
	}
	return s.Disabled.Reason
}

// TestCIPipeline (WU-046): stages, jobs, containers, matrix, needs,
// variables and every disabling construct of the CI fixture.
func TestCIPipeline(t *testing.T) {
	c, p := convert(t, "ci.yml")
	if p.On.Push == nil || !slices.Equal(p.On.Push.Branches, []string{"main", "release/**"}) || len(p.On.Push.Paths) != 0 || !p.On.Manual {
		t.Errorf("on = %+v push %+v", p.On, p.On.Push)
	}
	notes := strings.Join(c.Notes, "\n")
	for _, want := range []string{"exclude", "pr"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes miss %q:\n%s", want, notes)
		}
	}
	if p.Off != nil {
		t.Errorf("pipeline disabled: %+v", p.Off)
	}

	lint := step(t, p, "build-lint")
	if lint.Image != "node:20" || reason(lint) != "" || !slices.Equal(lint.Commands, []string{"# Install", "npm ci", "(cd web && npm run lint)"}) || lint.Env["NODE_ENV"] != "production" {
		t.Errorf("lint = %+v", lint)
	}
	if lint.Source == nil || lint.Source.File != "pipelines/ci.yml" || lint.Source.Line == 0 || len(lint.Source.Hash) != 64 {
		t.Errorf("lint source = %+v", lint.Source)
	}
	for _, entry := range []string{"api", "worker"} {
		s := step(t, p, "build-test-"+entry)
		if s.Env["SERVICE"] != entry || !slices.Equal(s.Commands, []string{"cd services/${SERVICE} && npm test"}) || !slices.Equal(s.Needs, []string{"build-lint"}) {
			t.Errorf("test %s = %+v", entry, s)
		}
	}
	if r := step(t, p, "build-report"); reason(r) != "task" || !strings.Contains(r.Disabled.Note, "PublishTestResults@2") || !slices.Equal(r.Needs, []string{"build-test-api", "build-test-worker"}) {
		t.Errorf("report = %+v", r)
	}
	integ := step(t, p, "go-integration")
	if reason(integ) != "docker" || !slices.Contains(integ.Needs, "build-report") || !slices.Contains(integ.Needs, "build-lint") {
		t.Errorf("integration = %+v (needs every job of Build)", integ)
	}
	if v := step(t, p, "go-version"); reason(v) != "variables" || !strings.Contains(v.Disabled.Note, "Build.BuildId") || strings.Contains(v.Disabled.Note, "git rev-parse") || strings.Contains(v.Disabled.Note, "Build.SourcesDirectory") {
		t.Errorf("version = %+v", v)
	}
	if s := step(t, p, "scan-secrets"); reason(s) != "host" || !strings.Contains(s.Disabled.Note, "ubuntu-latest") || len(s.Needs) != 0 {
		t.Errorf("secrets = %+v", s)
	}
	if g := step(t, p, "scan-gate"); reason(g) != "condition" {
		t.Errorf("gate = %+v", g)
	}
	var stages []string
	for _, sg := range p.Stages {
		stages = append(stages, sg.Name)
	}
	if !slices.Equal(stages, []string{"build", "go", "scan"}) {
		t.Errorf("stages = %v", stages)
	}
	if len(c.Steps) != len(p.Steps) {
		t.Errorf("step summaries %d, steps %d", len(c.Steps), len(p.Steps))
	}
}

// TestDeployPipeline (WU-046): tags disable the pipeline, and a parameter
// with a default is noted (WU-047); checkout of another repository,
// deployment jobs, templates and variable groups disable their steps.
func TestDeployPipeline(t *testing.T) {
	c, p := convert(t, "deploy.yml")
	if p.Off == nil || p.Off.Reason != "trigger" || !strings.Contains(p.Off.Note, "tags") || strings.Contains(p.Off.Note, "parameters") {
		t.Errorf("pipeline off = %+v", p.Off)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "target=test") {
		t.Errorf("notes = %v", c.Notes)
	}
	if p.On.Push != nil || !p.On.Manual {
		t.Errorf("on = %+v; a tag-only trigger has no branch push", p.On)
	}
	if b := step(t, p, "build"); reason(b) != "checkout" || !strings.Contains(b.Disabled.Note, "infra") || !strings.Contains(b.Disabled.Note, "DEPLOY_TOKEN") {
		t.Errorf("build = %+v", b)
	}
	if r := step(t, p, "release"); reason(r) != "deployment" || !strings.Contains(r.Disabled.Note, "production") || !slices.Equal(r.Needs, []string{"build"}) {
		t.Errorf("release = %+v", r)
	}
	if tp := step(t, p, "notify"); reason(tp) != "template" || !strings.Contains(tp.Disabled.Note, "jobs/notify.yml@infra") {
		t.Errorf("template = %+v", tp)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "prod-secrets") {
		t.Errorf("notes = %v", c.Notes)
	}
}

func TestStepsOnlyPipeline(t *testing.T) {
	_, p := convert(t, "steps-only.yml")
	if p.On.Push != nil || !p.On.Manual {
		t.Errorf("trigger: none → manual only; on = %+v", p.On)
	}
	s := step(t, p, "build")
	if s.Image != "python:3.12" || reason(s) != "" || !slices.Equal(s.Commands, []string{"pip install -r requirements.txt", "# Tests", "pytest -q"}) {
		t.Errorf("build = %+v", s)
	}
}

func TestDetect(t *testing.T) {
	for name, want := range map[string]bool{"ci.yml": true, "deploy.yml": true, "steps-only.yml": true, "notes.yml": false} {
		src, _ := os.ReadFile(filepath.Join("testdata", name))
		if got := (Source{}).Detect("x/"+name, src); got != want {
			t.Errorf("Detect(%s) = %v", name, got)
		}
	}
	if (Source{}).Detect(".plumbix/pipelines/x.yaml", []byte("steps: []\ntrigger: none\n")) {
		t.Error("a Plumbix pipeline was detected as Azure")
	}
	// Templates run only inside another pipeline.
	tpl := []byte("parameters:\n  - name: service\nsteps:\n  - script: echo ${{ parameters.service }}\nvariables:\n  a: b\n")
	for _, p := range []string{"pipelines/templates/build.yml", "ci/build-steps.yml"} {
		if (Source{}).Detect(p, tpl) {
			t.Errorf("template %s was detected as a pipeline", p)
		}
	}
	if (Source{}).Detect("pipelines/templates/deploy.yml", []byte("trigger: [main]\nsteps:\n  - script: x\n")) {
		t.Error("a file under templates/ was detected as a pipeline")
	}
}

// TestJobsWithTheSameSlugGetUniqueNames (WU-046 review): names are reserved
// as they are handed out, not only once steps exist.
func TestJobsWithTheSameSlugGetUniqueNames(t *testing.T) {
	src := "trigger: none\njobs:\n  - job: Build_App\n    container: x\n    steps:\n      - script: a\n  - job: build-app\n    container: x\n    steps:\n      - script: b\n"
	c, err := Source{}.Convert("p.yml", []byte(src), importer.Lookup{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.ParseTriggered(c.Content)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, c.Content)
	}
	if p.Steps[0].Name != "build-app" || p.Steps[1].Name != "build-app-2" {
		t.Errorf("names = %s, %s", p.Steps[0].Name, p.Steps[1].Name)
	}
}

// TestRepeatedJobKeys (WU-046 review): the same template twice, or two
// jobs with one name, still give one step each.
func TestRepeatedJobKeys(t *testing.T) {
	src := "trigger: none\njobs:\n  - template: jobs/notify.yml\n  - template: jobs/notify.yml\n"
	c, err := Source{}.Convert("p.yml", []byte(src), importer.Lookup{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.ParseTriggered(c.Content)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, c.Content)
	}
	if len(p.Steps) != 2 || p.Steps[0].Name != "notify" || p.Steps[1].Name != "notify-2" {
		t.Errorf("steps = %+v", p.Steps)
	}
}
