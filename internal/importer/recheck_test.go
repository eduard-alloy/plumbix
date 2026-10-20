package importer

import (
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// yamlSource converts a file by returning it: tests write the Plumbix
// pipeline the import would produce.
type yamlSource struct{}

func (yamlSource) Name() string                      { return "fake" }
func (yamlSource) Detect(path string, _ []byte) bool { return strings.HasSuffix(path, ".yml") }
func (yamlSource) Convert(_ string, src []byte, _ Lookup) (Converted, error) {
	if string(src) == "bad" {
		return Converted{}, errString("cannot read it")
	}
	return Converted{Content: src}, nil
}

func hashOf(c byte) string { return strings.Repeat(string(c), 64) }

// ciSource is a converted pipeline: build runs, deploy is disabled (host),
// both in stage main.
func ciSource(buildHash, buildRun, deploy string) string {
	return "# Imported from ci.yml\nversion: 1\non:\n  manual: true\nsteps:\n" +
		"  - name: build\n    image: golang\n    run: [" + buildRun + "]\n    source: {file: ci.yml, line: 3, hash: " + buildHash + "}\n" +
		deploy + stagesFor(deploy)
}

func stagesFor(deploy string) string {
	if deploy == "" {
		return "stages:\n  main: [build]\n"
	}
	return "stages:\n  main: [build, deploy]\n"
}

const hostDeploy = "  - name: deploy\n    needs: [build]\n    disabled: {reason: host, note: runs on the agent}\n    source: {file: ci.yml, line: 9, hash: " + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" + "}\n"
const runDeploy = "  - name: deploy\n    image: alpine\n    run: [./deploy.sh]\n    needs: [build]\n    source: {file: ci.yml, line: 9, hash: " + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" + "}\n"

func preview(t *testing.T, files map[string]string) Result {
	t.Helper()
	in := map[string][]byte{}
	for k, v := range files {
		in[k] = []byte(v)
	}
	r := Preview(yamlSource{}, Lookup{Files: in})
	if len(r.Problems) > 0 && files["bad.yml"] == "" {
		t.Fatalf("problems: %+v", r.Problems)
	}
	return r
}

// imported is what the first import of ci.yml writes.
func imported(t *testing.T) []byte {
	return preview(t, map[string]string{"ci.yml": ciSource(hashOf('a'), "go build", hostDeploy)}).Pipelines[0].Content
}

func notes(f FilePlan) string { return strings.Join(f.Notes, "\n") }

// TestPreviewStamps (WU-048): what the import writes carries hashes of
// itself, on the pipeline and on each step, and still parses.
func TestPreviewStamps(t *testing.T) {
	c := imported(t)
	p, err := pipeline.ParseTriggered(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, c)
	}
	if p.Imported == nil || p.Imported.System != "fake" || p.Imported.File != "ci.yml" || len(p.Imported.Written) != 64 {
		t.Errorf("pipeline source = %+v", p.Imported)
	}
	for _, s := range p.Steps {
		if s.Source == nil || len(s.Source.Written) != 64 {
			t.Errorf("step %s source = %+v", s.Name, s.Source)
		}
	}
	if !strings.HasPrefix(string(c), "# Imported from ci.yml\n") {
		t.Errorf("header comment lost:\n%s", c)
	}
	if again := preview(t, map[string]string{"ci.yml": ciSource(hashOf('a'), "go build", hostDeploy)}).Pipelines[0].Content; string(again) != string(c) {
		t.Errorf("preview is not deterministic")
	}
}

func recheck(t *testing.T, src map[string]string, existing map[string]string) Plan {
	t.Helper()
	ex := map[string][]byte{}
	for k, v := range existing {
		ex[k] = []byte(v)
	}
	return Recheck("fake", preview(t, src), ex)
}

func only(t *testing.T, p Plan) FilePlan {
	t.Helper()
	if len(p.Files) != 1 {
		t.Fatalf("files = %+v", p.Files)
	}
	f := p.Files[0]
	if f.Content != nil {
		if _, err := pipeline.ParseTriggered(f.Content); err != nil {
			t.Fatalf("%s does not parse: %v\n%s", f.Path, err, f.Content)
		}
	}
	return f
}

// TestRecheckFirstImportAndNothingChanged (WU-048).
func TestRecheckFirstImportAndNothingChanged(t *testing.T) {
	src := map[string]string{"ci.yml": ciSource(hashOf('a'), "go build", hostDeploy)}
	f := only(t, recheck(t, src, nil))
	if f.Path != ".plumbix/pipelines/ci.yaml" || f.Status != StatusAdded || f.SourceFile != "ci.yml" || string(f.Content) != string(imported(t)) {
		t.Errorf("first import = %+v", f)
	}
	p := recheck(t, src, map[string]string{".plumbix/pipelines/ci.yaml": string(imported(t))})
	if f := only(t, p); f.Status != StatusUnchanged || f.Content != nil || len(f.Notes) != 0 || p.Changed() {
		t.Errorf("re-check = %+v", f)
	}
}

// TestRecheckUpdatesWhatTheImportOwns (WU-048): a changed source job updates
// its step; a step whose reason is now supported runs; notes say so.
func TestRecheckUpdatesWhatTheImportOwns(t *testing.T) {
	ex := map[string]string{".plumbix/pipelines/ci.yaml": string(imported(t))}
	f := only(t, recheck(t, map[string]string{"ci.yml": ciSource(hashOf('b'), "go build ./...", runDeploy)}, ex))
	if f.Status != StatusUpdated || !strings.Contains(string(f.Content), "go build ./...") || !strings.Contains(string(f.Content), "./deploy.sh") {
		t.Fatalf("plan = %+v\n%s", f, f.Content)
	}
	for _, want := range []string{"build: updated, its source job changed", "deploy: now runs (was disabled: host)"} {
		if !strings.Contains(notes(f), want) {
			t.Errorf("notes lack %q: %v", want, f.Notes)
		}
	}
}

// TestRecheckKeepsEdits (WU-048): a step a person edited is kept, and so is
// a step of their own with its stage; edited pipeline fields are kept.
func TestRecheckKeepsEdits(t *testing.T) {
	edited := strings.Replace(string(imported(t)), "go build", "go build -race", 1)
	edited = strings.Replace(edited, "manual: true", "manual: true\n  push:\n    branches: [main]", 1)
	edited = strings.Replace(edited, "stages:\n", "  - name: lint\n    image: golang\n    run: [go vet]\nstages:\n  checks: [lint]\n", 1)
	f := only(t, recheck(t, map[string]string{"ci.yml": ciSource(hashOf('b'), "go build ./...", runDeploy)}, map[string]string{".plumbix/pipelines/ci.yaml": edited}))
	c := string(f.Content)
	if f.Status != StatusUpdated || !strings.Contains(c, "go build -race") || strings.Contains(c, "go build ./...") || !strings.Contains(c, "./deploy.sh") || !strings.Contains(c, "go vet") || !strings.Contains(c, "branches: [main]") {
		t.Fatalf("plan = %+v\n%s", f, c)
	}
	p, _ := pipeline.ParseTriggered(f.Content)
	stage := map[string]string{}
	for _, s := range p.Steps {
		stage[s.Name] = s.Stage
	}
	if stage["lint"] != "checks" || stage["build"] != "main" || stage["deploy"] != "main" {
		t.Errorf("stages = %v", stage)
	}
	for _, want := range []string{"build: kept as you edited it; its source job changed since the import", "pipeline settings: kept as you edited them"} {
		if !strings.Contains(notes(f), want) {
			t.Errorf("notes lack %q: %v", want, f.Notes)
		}
	}
	// The kept edits stay edits on the next re-check.
	g := only(t, recheck(t, map[string]string{"ci.yml": ciSource(hashOf('b'), "go build ./...", runDeploy)}, map[string]string{".plumbix/pipelines/ci.yaml": c}))
	if g.Status != StatusUnchanged {
		t.Errorf("second re-check = %+v\n%s", g, g.Content)
	}
}

// TestRecheckGoneSources (WU-048): a gone source pipeline removes its file
// when untouched and keeps it when edited; a gone job's step goes unless a
// kept step needs it; a source that fails now keeps its file.
func TestRecheckGoneSources(t *testing.T) {
	other := map[string]string{"other.yml": ciSource(hashOf('a'), "make", hostDeploy)}
	f := recheck(t, other, map[string]string{".plumbix/pipelines/ci.yaml": string(imported(t))})
	var gone FilePlan
	for _, x := range f.Files {
		if x.Path == ".plumbix/pipelines/ci.yaml" {
			gone = x
		}
	}
	if gone.Status != StatusRemoved || len(f.Files) != 2 {
		t.Errorf("untouched gone = %+v", f.Files)
	}
	edited := strings.Replace(string(imported(t)), "go build", "go build -race", 1)
	f = recheck(t, other, map[string]string{".plumbix/pipelines/ci.yaml": edited})
	for _, x := range f.Files {
		if x.Path == ".plumbix/pipelines/ci.yaml" && (x.Status != StatusKept || !strings.Contains(notes(x), "ci.yml is gone")) {
			t.Errorf("edited gone = %+v", x)
		}
	}

	noDeploy := ciSource(hashOf('a'), "go build", "")
	x := only(t, recheck(t, map[string]string{"ci.yml": noDeploy}, map[string]string{".plumbix/pipelines/ci.yaml": string(imported(t))}))
	if x.Status != StatusUpdated || strings.Contains(string(x.Content), "deploy") || !strings.Contains(notes(x), "deploy: removed, its job is gone from the source") {
		t.Errorf("gone job = %+v\n%s", x, x.Content)
	}
	noBuild := "version: 1\non:\n  manual: true\nsteps:\n" + strings.Replace(hostDeploy, "    needs: [build]\n", "", 1)
	withNeed := strings.Replace(string(imported(t)), "    disabled: {reason: host", "    run: [./deploy.sh]\n    image: alpine\n    disabled: {reason: user", 1)
	x = only(t, recheck(t, map[string]string{"ci.yml": noBuild}, map[string]string{".plumbix/pipelines/ci.yaml": withNeed}))
	if !strings.Contains(string(x.Content), "name: build") || !strings.Contains(notes(x), "build: its job is gone from the source; kept because deploy needs it") {
		t.Errorf("needed gone job = %+v\n%s", x, x.Content)
	}

	x = only(t, recheck(t, map[string]string{"ci.yml": "bad", "bad.yml": "x"}, map[string]string{".plumbix/pipelines/ci.yaml": string(imported(t))}))
	if x.Status != StatusKept || !strings.Contains(notes(x), "cannot read it") {
		t.Errorf("failed source = %+v", x)
	}
}

// TestRecheckNames (WU-048): a renamed imported file is still matched, and
// a new pipeline avoids the name of a person's own file.
func TestRecheckNames(t *testing.T) {
	src := map[string]string{"ci.yml": ciSource(hashOf('b'), "go build", hostDeploy)}
	f := only(t, recheck(t, src, map[string]string{".plumbix/pipelines/my-ci.yaml": string(imported(t))}))
	if f.Path != ".plumbix/pipelines/my-ci.yaml" || f.Status != StatusUpdated {
		t.Errorf("renamed = %+v", f)
	}
	own := "version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n"
	f = only(t, recheck(t, src, map[string]string{".plumbix/pipelines/ci.yaml": own}))
	if f.Path != ".plumbix/pipelines/ci-2.yaml" || f.Status != StatusAdded {
		t.Errorf("collision = %+v", f)
	}
	dup := strings.Replace(string(imported(t)), "    image: golang\n", "    image: golang\n    image: alpine\n", 1)
	f = only(t, recheck(t, src, map[string]string{".plumbix/pipelines/ci.yaml": dup}))
	if f.Status != StatusKept || !strings.Contains(notes(f), "not valid YAML") {
		t.Errorf("duplicate key = %+v", f)
	}
	f = only(t, recheck(t, src, map[string]string{".plumbix/pipelines/ci.yaml": "steps: [unclosed"}))
	if f.Path != ".plumbix/pipelines/ci-2.yaml" {
		t.Errorf("unparsable own file = %+v", f)
	}
}

// TestPlanWrites (WU-048): the files a plan commits, nil for removals.
func TestPlanWrites(t *testing.T) {
	p := Plan{Files: []FilePlan{
		{Path: "a", Status: StatusAdded, Content: []byte("x")},
		{Path: "b", Status: StatusRemoved},
		{Path: "c", Status: StatusKept},
		{Path: "d", Status: StatusUnchanged},
	}}
	w := p.Writes()
	if len(w) != 2 || string(w["a"]) != "x" || w["b"] != nil || !p.Changed() {
		t.Errorf("writes = %v", w)
	}
	if (Plan{Files: []FilePlan{{Status: StatusKept}}}).Changed() {
		t.Error("a kept file is not a change")
	}
}
