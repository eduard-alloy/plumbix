package pipeline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const schemaPath = "../../contracts/pipeline.schema.json"

// TestSchemaAndParserAgree keeps the published schema and the parser in
// step. Valid files pass both. Structural problems fail both. Cross-field
// problems pass the schema, which cannot express them, and fail the parser.
func TestSchemaAndParserAgree(t *testing.T) {
	schema := compileSchema(t)
	groups := []struct {
		dir                       string
		wantSchemaOK, wantParseOK bool
	}{
		{"testdata/valid", true, true},
		{"testdata/invalid-structure", false, false},
		{"testdata/invalid-crossfield", true, false},
	}
	for _, g := range groups {
		files, err := filepath.Glob(filepath.Join(g.dir, "*.yaml"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no example files in %s", g.dir)
		}
		for _, f := range files {
			t.Run(f, func(t *testing.T) {
				src, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				schemaErr := schema.Validate(yamlToJSONValue(t, src))
				if (schemaErr == nil) != g.wantSchemaOK {
					t.Errorf("schema valid = %v, want %v (%v)", schemaErr == nil, g.wantSchemaOK, schemaErr)
				}
				_, parseErr := Parse(src)
				if (parseErr == nil) != g.wantParseOK {
					t.Errorf("parser valid = %v, want %v (%v)", parseErr == nil, g.wantParseOK, parseErr)
				}
			})
		}
	}
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("pipeline.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("pipeline.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// yamlToJSONValue converts YAML into the JSON value the schema library expects.
func yamlToJSONValue(t *testing.T, src []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(src, &v); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
