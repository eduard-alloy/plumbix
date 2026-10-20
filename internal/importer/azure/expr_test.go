package azure

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func testScope(t *testing.T) *scope {
	t.Helper()
	var doc yaml.Node
	src := "env: prod\ncount: 3\nflag: true\nlist: [a, b]\nobj: {name: n, nested: {x: 1}}\n"
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	s := &scope{params: map[string]any{}, vars: map[string]string{"region": "eu"}}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		s.params[root.Content[i].Value] = nodeValue(root.Content[i+1])
	}
	return s
}

// TestExpressions (WU-047): references, literals and the supported
// functions evaluate as Azure does, rendered as strings.
func TestExpressions(t *testing.T) {
	s := testScope(t)
	for expr, want := range map[string]string{
		"parameters.env":                           "prod",
		"parameters['env']":                        "prod",
		"parameters.count":                         "3",
		"parameters.flag":                          "True",
		"parameters.obj.nested.x":                  "1",
		"parameters['obj'].name":                   "n",
		"variables.region":                         "eu",
		"variables['region']":                      "eu",
		"variables.missing":                        "",
		"'it''s'":                                  "it's",
		"null":                                     "",
		"1.5":                                      "1.5",
		"eq(parameters.env, 'PROD')":               "True",
		"ne(parameters.env, 'prod')":               "False",
		"eq(parameters.count, '3')":                "True",
		"eq(parameters.flag, 'true')":              "True",
		"and(true, not(false))":                    "True",
		"or(false, eq(1, 1))":                      "True",
		"xor(true, false)":                         "True",
		"in(parameters.env, 'dev', 'prod')":        "True",
		"notIn(parameters.env, 'dev', 'test')":     "True",
		"contains('abc', 'B')":                     "True",
		"containsValue(parameters.list, 'b')":      "True",
		"startsWith('release/1', 'Release/')":      "True",
		"endsWith('a.yml', '.YML')":                "True",
		"coalesce('', null, parameters.env)":       "prod",
		"format('{0}-{1}', 'a', parameters.count)": "a-3",
		"join(',', parameters.list)":               "a,b",
		"lower('AB')":                              "ab",
		"upper('ab')":                              "AB",
		"replace('a-b-c', '-', '_')":               "a_b_c",
		"length(parameters.list)":                  "2",
		"length('abc')":                            "3",
		"gt(parameters.count, 2)":                  "True",
		"le(parameters.count, 2)":                  "False",
		"Eq(1, 1)":                                 "True",
	} {
		v, err := evalExpr(expr, s)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		got, err := toString(v)
		if err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", expr, got, err, want)
		}
	}
}

// TestExpressionErrors (WU-047): what Plumbix cannot know at import time,
// unknown functions, undeclared parameters and bad syntax are errors.
func TestExpressionErrors(t *testing.T) {
	s := testScope(t)
	for expr, want := range map[string]string{
		"variables['Build.SourceBranch']": "Build.SourceBranch",
		"variables.Build.Reason":          "Build.Reason",
		"counter('x', 1)":                 "counter",
		"parameters.nope":                 "nope",
		"eq(1,":                           "",
		"dependencies.a.result":           "dependencies",
		"parameters.list.x":               "x",
		"in('a')":                         "at least 2",
		"notIn('a')":                      "notIn",
	} {
		_, err := evalExpr(expr, s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want one naming %q", expr, err, want)
		}
	}
}

// TestTruthiness (WU-047): empty strings, zero, false and null are false.
func TestTruthiness(t *testing.T) {
	for v, want := range map[any]bool{"": false, "x": true, 0.0: false, 2.0: true, false: false, true: true, nil: false} {
		if truthy(v) != want {
			t.Errorf("truthy(%#v) = %v", v, !want)
		}
	}
}
