package pipeline

import (
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// source parses an imported step's origin.
func (ps *parser) source(p string, n *yaml.Node) *Source {
	if n.Kind != yaml.MappingNode {
		ps.add(p, n.Line, "must be a mapping with file, line and hash")
		return nil
	}
	s := &Source{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := p + "." + key.Value
		seen[key.Value] = true
		switch key.Value {
		case "file":
			if v, ok := ps.str(fp, val); ok {
				if v == "" {
					ps.add(fp, val.Line, "must not be empty")
				}
				s.File = v
			}
		case "line":
			v, err := strconv.Atoi(val.Value)
			if val.Kind != yaml.ScalarNode || val.Tag != "!!int" || err != nil || v < 1 {
				ps.add(fp, val.Line, "must be a positive integer")
			} else {
				s.Line = v
			}
		case "hash":
			if v, ok := ps.str(fp, val); ok {
				if !hashRe.MatchString(v) {
					ps.add(fp, val.Line, "must be 64 hex characters (SHA-256)")
				}
				s.Hash = v
			}
		default:
			ps.add(fp, key.Line, "unknown field")
		}
	}
	for _, req := range []string{"file", "line", "hash"} {
		if !seen[req] {
			ps.add(p+"."+req, n.Line, "is required")
		}
	}
	return s
}
