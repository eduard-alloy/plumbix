package importer

import (
	"strings"
	"testing"
)

func TestPipelineName(t *testing.T) {
	for in, want := range map[string]string{
		"pipelines/azure-pipelines-orders-api.yml": "orders-api",
		"azure-pipelines.yml":                      "azure-pipelines",
		"ci/Build_And Test.yaml":                   "build-and-test",
		"azure-pipelines.deploy.yml":               "deploy",
		"x/---.yml":                                "pipeline",
	} {
		if got := PipelineName(in); got != want {
			t.Errorf("PipelineName(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeSource struct{}

func (fakeSource) Name() string                      { return "fake" }
func (fakeSource) Detect(path string, _ []byte) bool { return strings.HasSuffix(path, ".yml") }
func (fakeSource) Convert(path string, src []byte) (Converted, error) {
	if string(src) == "bad" {
		return Converted{}, errString("cannot read it")
	}
	return Converted{Content: []byte("version: 1\non: {manual: true}\nsteps:\n  - {name: a, image: x, run: x}\n")}, nil
}

type errString string

func (e errString) Error() string { return string(e) }

// TestPreviewNamesSortsAndChecks (WU-046): names are unique, files sorted,
// failures become problems, and every pipeline is checked to parse.
func TestPreviewNamesSortsAndChecks(t *testing.T) {
	r := Preview(fakeSource{}, map[string][]byte{
		"b/ci.yml":  []byte("ok"),
		"a/ci.yml":  []byte("ok"),
		"bad.yml":   []byte("bad"),
		"notes.txt": []byte("ok"),
	})
	var names []string
	for _, p := range r.Pipelines {
		names = append(names, p.Name+"="+p.SourceFile+"->"+p.File)
	}
	want := "ci=a/ci.yml->.plumbix/pipelines/ci.yaml,ci-2=b/ci.yml->.plumbix/pipelines/ci-2.yaml"
	if strings.Join(names, ",") != want {
		t.Errorf("pipelines = %v", names)
	}
	if len(r.Problems) != 1 || r.Problems[0].SourceFile != "bad.yml" || !strings.Contains(r.Problems[0].Message, "cannot read it") {
		t.Errorf("problems = %+v", r.Problems)
	}
}
