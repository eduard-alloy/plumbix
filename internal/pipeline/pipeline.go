// Package pipeline parses and validates Plumbix pipeline files
// (.plumbix/pipeline.yaml, format version 1) and orders their steps.
package pipeline

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// FormatVersion is the only pipeline format version this package accepts.
const FormatVersion = 1

// MaxTimeout is the longest timeout a step may declare.
const MaxTimeout = 24 * time.Hour

// Pipeline is a validated pipeline file.
type Pipeline struct {
	Version int
	Steps   []Step
	order   []string
}

// Step is one validated pipeline step.
type Step struct {
	Name      string
	Image     string
	Run       string
	Needs     []string
	Env       map[string]string
	Secrets   []string
	Artifacts []string
	Timeout   time.Duration // zero means the server default
	Validator bool
	Line      int
}

// Order returns step names so that every step comes after the steps it
// needs. Steps that become ready together keep their file order.
func (p *Pipeline) Order() []string {
	return append([]string(nil), p.order...)
}

var (
	stepNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	varNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const reservedPrefix = "PLUMBIX_"

// parser collects every problem instead of stopping at the first.
type parser struct {
	errs []Error
}

func (ps *parser) add(path string, line int, format string, args ...any) {
	ps.errs = append(ps.errs, Error{Path: path, Line: line, Message: fmt.Sprintf(format, args...)})
}

// stepRefs remembers where each needs entry appeared, for cross-field checks.
type stepRefs struct {
	needLines []int
}

// Parse validates src and returns the pipeline, or a *ValidationError
// listing every problem found.
func Parse(src []byte) (*Pipeline, error) {
	ps := &parser{}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		ps.add("", 0, "invalid yaml: %v", err)
		return nil, &ValidationError{Errors: ps.errs}
	}
	if len(doc.Content) == 0 {
		ps.add("", 0, "pipeline file is empty")
		return nil, &ValidationError{Errors: ps.errs}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		ps.add("", root.Line, "pipeline file must be a mapping with version and steps")
		return nil, &ValidationError{Errors: ps.errs}
	}

	p := &Pipeline{}
	var stepsKey, stepsNode *yaml.Node
	var sawVersion bool
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "version":
			sawVersion = true
			p.Version = ps.version(val)
		case "steps":
			stepsKey, stepsNode = key, val
		default:
			ps.add(key.Value, key.Line, "unknown field")
		}
	}
	if !sawVersion {
		ps.add("version", root.Line, "is required")
	}

	var refs []stepRefs
	switch {
	case stepsNode == nil:
		ps.add("steps", root.Line, "is required")
	case stepsNode.Kind != yaml.SequenceNode:
		ps.add("steps", stepsNode.Line, "must be a list")
	case len(stepsNode.Content) == 0:
		ps.add("steps", stepsKey.Line, "must contain at least one step")
	default:
		for i, item := range stepsNode.Content {
			st, r := ps.step(fmt.Sprintf("steps[%d]", i), item)
			p.Steps = append(p.Steps, st)
			refs = append(refs, r)
		}
		ps.crossCheck(p.Steps, refs)
		p.order = ps.order(p.Steps, stepsKey.Line)
	}

	if len(ps.errs) > 0 {
		return nil, &ValidationError{Errors: ps.errs}
	}
	return p, nil
}

func (ps *parser) version(n *yaml.Node) int {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		ps.add("version", n.Line, "must be the integer 1")
		return 0
	}
	if n.Value != "1" {
		ps.add("version", n.Line, "must be 1, got %s", n.Value)
		return 0
	}
	return FormatVersion
}

func (ps *parser) step(p string, n *yaml.Node) (Step, stepRefs) {
	st := Step{Line: n.Line}
	var refs stepRefs
	if n.Kind != yaml.MappingNode {
		ps.add(p, n.Line, "must be a mapping")
		return st, refs
	}
	seen := map[string]bool{}
	var envNode, secretsNode *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := p + "." + key.Value
		seen[key.Value] = true
		switch key.Value {
		case "name":
			if s, ok := ps.str(fp, val); ok && !stepNameRe.MatchString(s) {
				ps.add(fp, val.Line, "must be lowercase letters, digits and dashes, start and end with a letter or digit, at most 63 characters")
			} else {
				st.Name = s
			}
		case "image":
			if s, ok := ps.str(fp, val); ok {
				if s == "" {
					ps.add(fp, val.Line, "is required and must not be empty")
				} else if strings.ContainsAny(s, " \t\r\n") {
					ps.add(fp, val.Line, "must not contain whitespace")
				}
				st.Image = s
			}
		case "run":
			if s, ok := ps.str(fp, val); ok {
				if s == "" {
					ps.add(fp, val.Line, "is required and must not be empty")
				}
				st.Run = s
			}
		case "validator":
			if val.Kind != yaml.ScalarNode || val.Tag != "!!bool" {
				ps.add(fp, val.Line, "must be a boolean")
			} else {
				st.Validator = val.Value == "true"
			}
		case "needs":
			for j, item := range ps.list(fp, val) {
				if s, ok := ps.str(fmt.Sprintf("%s[%d]", fp, j), item); ok {
					st.Needs = append(st.Needs, s)
					refs.needLines = append(refs.needLines, item.Line)
				}
			}
		case "env":
			envNode = val
			st.Env = ps.env(fp, val)
		case "secrets":
			secretsNode = val
			st.Secrets = ps.secrets(fp, val)
		case "artifacts":
			st.Artifacts = ps.artifacts(fp, val)
		case "timeout":
			st.Timeout = ps.timeout(fp, val)
		default:
			ps.add(fp, key.Line, "unknown field")
		}
	}
	for _, req := range []string{"name", "image"} {
		if !seen[req] {
			ps.add(p+"."+req, n.Line, "is required")
		}
	}
	if !seen["run"] && !st.Validator {
		ps.add(p+".run", n.Line, "is required for steps that are not validators")
	}
	if envNode != nil && secretsNode != nil {
		for j, item := range secretsNode.Content {
			if _, clash := st.Env[item.Value]; clash {
				ps.add(fmt.Sprintf("%s.secrets[%d]", p, j), item.Line, "%q is also set in env", item.Value)
			}
		}
	}
	return st, refs
}

func (ps *parser) str(p string, n *yaml.Node) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		ps.add(p, n.Line, "must be a string")
		return "", false
	}
	return n.Value, true
}

func (ps *parser) list(p string, n *yaml.Node) []*yaml.Node {
	if n.Kind != yaml.SequenceNode {
		ps.add(p, n.Line, "must be a list")
		return nil
	}
	return n.Content
}

func (ps *parser) env(p string, n *yaml.Node) map[string]string {
	if n.Kind != yaml.MappingNode {
		ps.add(p, n.Line, "must be a mapping of names to string values")
		return nil
	}
	env := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := p + "." + key.Value
		ps.varName(fp, key)
		if s, ok := ps.str(fp, val); ok {
			env[key.Value] = s
		}
	}
	return env
}

func (ps *parser) secrets(p string, n *yaml.Node) []string {
	var out []string
	seen := map[string]bool{}
	for j, item := range ps.list(p, n) {
		fp := fmt.Sprintf("%s[%d]", p, j)
		s, ok := ps.str(fp, item)
		if !ok || !ps.varName(fp, item) {
			continue
		}
		if seen[s] {
			ps.add(fp, item.Line, "duplicate secret %q", s)
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// varName checks an environment variable or secret name.
func (ps *parser) varName(p string, n *yaml.Node) bool {
	switch {
	case !varNameRe.MatchString(n.Value):
		ps.add(p, n.Line, "must be a valid variable name: letters, digits and underscores, not starting with a digit")
		return false
	case strings.HasPrefix(n.Value, reservedPrefix):
		ps.add(p, n.Line, "names starting with %s are reserved for Plumbix", reservedPrefix)
		return false
	}
	return true
}

func (ps *parser) artifacts(p string, n *yaml.Node) []string {
	var out []string
	for j, item := range ps.list(p, n) {
		fp := fmt.Sprintf("%s[%d]", p, j)
		s, ok := ps.str(fp, item)
		if !ok {
			continue
		}
		switch {
		case s == "":
			ps.add(fp, item.Line, "must not be empty")
		case path.IsAbs(s) || strings.HasPrefix(s, `\`):
			ps.add(fp, item.Line, "must be a relative path inside the workspace")
		case hasDotDot(s):
			ps.add(fp, item.Line, "must not contain .. segments")
		default:
			out = append(out, s)
		}
	}
	return out
}

func hasDotDot(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

func (ps *parser) timeout(p string, n *yaml.Node) time.Duration {
	s, ok := ps.str(p, n)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		ps.add(p, n.Line, "must be a duration such as 90s or 10m")
	case d <= 0:
		ps.add(p, n.Line, "must be above zero")
	case d > MaxTimeout:
		ps.add(p, n.Line, "must be at most 24h")
	default:
		return d
	}
	return 0
}

// crossCheck enforces rules that span steps: unique names and valid needs.
func (ps *parser) crossCheck(steps []Step, refs []stepRefs) {
	index := map[string]int{}
	for i, st := range steps {
		if st.Name == "" {
			continue
		}
		if _, dup := index[st.Name]; dup {
			ps.add(fmt.Sprintf("steps[%d].name", i), st.Line, "duplicate step name %q", st.Name)
			continue
		}
		index[st.Name] = i
	}
	for i, st := range steps {
		seen := map[string]bool{}
		for j, need := range st.Needs {
			fp := fmt.Sprintf("steps[%d].needs[%d]", i, j)
			line := refs[i].needLines[j]
			target, known := index[need]
			switch {
			case need == st.Name:
				ps.add(fp, line, "a step cannot need itself")
			case !known:
				ps.add(fp, line, "unknown step %q", need)
			case seen[need]:
				ps.add(fp, line, "duplicate need %q", need)
			case steps[target].Validator:
				ps.add(fp, line, "cannot need validator step %q: verdicts are final outputs", need)
			}
			seen[need] = true
		}
	}
}

// order sorts steps into dependency levels, keeping file order inside each
// level. Steps left over form a cycle and are reported.
func (ps *parser) order(steps []Step, line int) []string {
	done := map[string]bool{}
	known := map[string]bool{}
	for _, st := range steps {
		known[st.Name] = true
	}
	var out []string
	remaining := steps
	for len(remaining) > 0 {
		var ready, blocked []Step
		for _, st := range remaining {
			if needsMet(st, done, known) {
				ready = append(ready, st)
			} else {
				blocked = append(blocked, st)
			}
		}
		if len(ready) == 0 {
			names := make([]string, len(blocked))
			for i, st := range blocked {
				names[i] = st.Name
			}
			ps.add("steps", line, "dependency cycle between steps: %s", strings.Join(names, ", "))
			return nil
		}
		for _, st := range ready {
			done[st.Name] = true
			out = append(out, st.Name)
		}
		remaining = blocked
	}
	return out
}

// needsMet reports whether every known need of st is done. Unknown needs
// are reported elsewhere and ignored here.
func needsMet(st Step, done, known map[string]bool) bool {
	for _, n := range st.Needs {
		if known[n] && n != st.Name && !done[n] {
			return false
		}
	}
	return true
}
