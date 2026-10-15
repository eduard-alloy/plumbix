package validation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/eduard-alloy/plumbix/internal/change"
)

const contractsBase = "https://github.com/eduard-alloy/plumbix/blob/main/contracts/"

// compileSchemaRef compiles a definition from contracts/<file>. Every schema
// in contracts/ is loaded under its $id, so references between them resolve
// without network access.
func compileSchemaRef(t *testing.T, file, ref string) *jsonschema.Schema {
	t.Helper()
	paths, err := filepath.Glob("../../contracts/*.schema.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no schemas found: %v", err)
	}
	c := jsonschema.NewCompiler()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if err := c.AddResource(contractsBase+filepath.Base(p), doc); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Compile(contractsBase + file + ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// compileSchema compiles a whole schema file from contracts/.
func compileSchema(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()
	return compileSchemaRef(t, file, "")
}

func validateJSON(t *testing.T, s *jsonschema.Schema, data []byte) error {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// TestVerdictSchemaAndParserAgree keeps verdict.schema.json and ParseVerdict
// in step over example files. Cross-field files pass the schema, which can't
// know the change's criteria, and fail the parser.
func TestVerdictSchemaAndParserAgree(t *testing.T) {
	schema := compileSchema(t, "verdict.schema.json")
	crit := []change.Criterion{{ID: "AC-1", Text: "a"}, {ID: "AC-2", Text: "b"}, {ID: "AC-3", Text: "c"}}
	groups := []struct {
		dir            string
		schemaOK, goOK bool
	}{
		{"testdata/verdicts/valid", true, true},
		{"testdata/verdicts/invalid-structure", false, false},
		{"testdata/verdicts/invalid-crossfield", true, false},
	}
	for _, g := range groups {
		files, _ := filepath.Glob(filepath.Join(g.dir, "*.json"))
		if len(files) == 0 {
			t.Fatalf("no examples in %s", g.dir)
		}
		for _, f := range files {
			t.Run(f, func(t *testing.T) {
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				schemaErr := validateJSON(t, schema, data)
				if (schemaErr == nil) != g.schemaOK {
					t.Errorf("schema valid = %v, want %v (%v)", schemaErr == nil, g.schemaOK, schemaErr)
				}
				_, goErr := ParseVerdict(data, crit)
				if (goErr == nil) != g.goOK {
					t.Errorf("parser valid = %v, want %v (%v)", goErr == nil, g.goOK, goErr)
				}
			})
		}
	}
}
