package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const openAPIPath = "../../contracts/openapi.yaml"

// openAPISchemas compiles schemas from the OpenAPI file's components.
type openAPISchemas struct {
	t *testing.T
	c *jsonschema.Compiler
}

func loadOpenAPI(t *testing.T) *openAPISchemas {
	t.Helper()
	src, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("openapi.json", parsed); err != nil {
		t.Fatal(err)
	}
	return &openAPISchemas{t: t, c: c}
}

// check validates a response body against components/schemas/<name>.
func (o *openAPISchemas) check(name string, rec *httptest.ResponseRecorder) {
	o.t.Helper()
	s, err := o.c.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		o.t.Fatalf("compile %s: %v", name, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		o.t.Fatalf("%s: body is not JSON: %s", name, rec.Body.String())
	}
	if err := s.Validate(v); err != nil {
		o.t.Errorf("response does not match %s:\n%s\n%v", name, rec.Body.String(), err)
	}
}

// TestResponsesMatchOpenAPI sends real requests and checks every response
// body against the published schema for that response.
func TestResponsesMatchOpenAPI(t *testing.T) {
	o := loadOpenAPI(t)
	a := newTestAPI(t)

	o.check("Health", a.do(http.MethodGet, "/api/v1/health", ""))

	repoRec := a.do(http.MethodPost, "/api/v1/repos", repoBody)
	o.check("Repo", repoRec)
	o.check("RepoList", a.do(http.MethodGet, "/api/v1/repos", ""))
	o.check("Error", a.do(http.MethodPost, "/api/v1/repos", repoBody))
	repoID := decode[struct{ ID string }](t, repoRec).ID

	body := `{"repo_id": "` + repoID + `", "source_branch": "feat", "target_branch": "main", "title": "T", "intent": "I",
	  "constraints": ["no new deps"], "submitted_by": "kai",
	  "acceptance_criteria": [{"text": "a", "check": {"steps": ["test"]}}, {"text": "b", "check": {"command": "make lint"}}, {"text": "c"}]}`
	changeRec := a.do(http.MethodPost, "/api/v1/changes", body)
	o.check("Change", changeRec)
	id := decode[struct{ ID string }](t, changeRec).ID
	o.check("Change", a.do(http.MethodGet, "/api/v1/changes/"+id, ""))
	o.check("ChangeList", a.do(http.MethodGet, "/api/v1/changes", ""))
	o.check("Change", a.do(http.MethodPost, "/api/v1/changes/"+id+"/close", ""))

	o.check("Error", a.do(http.MethodPost, "/api/v1/changes", `{}`))
	o.check("Error", a.do(http.MethodGet, "/api/v1/changes/c_aaaaaaaaaaaa", ""))
	o.check("Error", a.do(http.MethodPost, "/api/v1/changes/"+id+"/close", ""))

	o.check("PipelineCheck", a.do(http.MethodPost, "/api/v1/pipelines/check", "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n"))
	o.check("PipelineCheck", a.do(http.MethodPost, "/api/v1/pipelines/check", "version: 2\n"))

	unauth := httptest.NewRecorder()
	a.h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v1/changes", strings.NewReader("")))
	o.check("Error", unauth)
}
