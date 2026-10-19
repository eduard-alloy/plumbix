package pipeline

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Triggers say what starts a triggered pipeline (WU-045).
type Triggers struct {
	Push   *Push
	Manual bool
}

// Push starts a pipeline when a matching branch moves.
type Push struct {
	// Branches are globs over branch names, as in when.paths.
	Branches []string
	// Paths, when set, also require a changed file to match one of them.
	Paths []string
}

// Matches reports whether a push to branch starts the pipeline. changed are
// the files changed since the branch's previous head; isNew says the branch
// had none, and then the paths are not consulted.
func (p *Push) Matches(branch string, changed []string, isNew bool) bool {
	hit := false
	for _, g := range p.Branches {
		if MatchPath(g, branch) {
			hit = true
			break
		}
	}
	if !hit || len(p.Paths) == 0 || isNew {
		return hit
	}
	for _, f := range changed {
		for _, g := range p.Paths {
			if MatchPath(g, f) {
				return true
			}
		}
	}
	return false
}

func (ps *parser) on(n *yaml.Node) *Triggers {
	if n.Kind != yaml.MappingNode {
		ps.add("on", n.Line, "must be a mapping with push, manual or both")
		return nil
	}
	t := &Triggers{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := "on." + key.Value
		switch key.Value {
		case "push":
			t.Push = ps.push(fp, val)
		case "manual":
			if val.Kind != yaml.ScalarNode || val.Tag != "!!bool" {
				ps.add(fp, val.Line, "must be a boolean")
			} else {
				t.Manual = val.Value == "true"
			}
		default:
			ps.add(fp, key.Line, "unknown field")
		}
	}
	if t.Push == nil && !t.Manual {
		ps.add("on", n.Line, "must name push or manual: true")
	}
	return t
}

func (ps *parser) push(p string, n *yaml.Node) *Push {
	if n.Kind != yaml.MappingNode {
		ps.add(p, n.Line, "must be a mapping with branches and optional paths")
		return nil
	}
	out := &Push{}
	sawBranches := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := p + "." + key.Value
		switch key.Value {
		case "branches":
			sawBranches = true
			out.Branches = ps.globs(fp, val)
		case "paths":
			out.Paths = ps.globs(fp, val)
		default:
			ps.add(fp, key.Line, "unknown field")
		}
	}
	if !sawBranches {
		ps.add(p+".branches", n.Line, "is required")
	}
	return out
}

// globs reads a non-empty list of relative globs.
func (ps *parser) globs(p string, n *yaml.Node) []string {
	items := ps.list(p, n)
	if n.Kind == yaml.SequenceNode && len(items) == 0 {
		ps.add(p, n.Line, "must contain at least one glob")
	}
	var out []string
	for j, item := range items {
		ip := fmt.Sprintf("%s[%d]", p, j)
		s, ok := ps.str(ip, item)
		if !ok {
			continue
		}
		switch {
		case s == "":
			ps.add(ip, item.Line, "must not be empty")
		case path.IsAbs(s) || strings.HasPrefix(s, `\`):
			ps.add(ip, item.Line, "must be a relative glob")
		case !validGlob(s):
			ps.add(ip, item.Line, "is not a valid glob")
		default:
			out = append(out, s)
		}
	}
	return out
}
