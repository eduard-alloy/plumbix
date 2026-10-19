package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxDisabledNote is the longest note a disabled step may carry.
const MaxDisabledNote = 500

// Disabled says why a step does not run (WU-044, A36): imported steps
// Plumbix cannot run yet, or steps a person switched off.
type Disabled struct {
	Reason string
	Note   string
}

// Reason is one published code for a disabled step.
type Reason struct {
	Code        string
	Description string
}

// Reasons are the codes a disabled step may name, in the schema's order.
var Reasons = []Reason{
	{"user", "Disabled by a person."},
	{"docker", "Uses Docker itself (docker run, docker build); steps have no Docker access yet."},
	{"services", "Needs service containers, such as a database or a cache, beside it."},
	{"host", "Runs on the build agent's machine, with no container image."},
	{"task", "A task of the source CI system that Plumbix has no equivalent for."},
	{"template", "Comes from a template Plumbix could not read."},
	{"condition", "Runs under a condition Plumbix cannot express."},
	{"checkout", "Needs another repository checked out."},
	{"deployment", "Deploys to an environment or waits for an approval."},
	{"secrets", "Needs variables or secret groups that are not mapped to Plumbix secrets."},
	{"variables", "Uses variables or expressions of the source CI system that Plumbix cannot resolve."},
}

// PipelineReasons are the codes a whole pipeline may be disabled with: the
// step reasons plus trigger (WU-045).
var PipelineReasons = append(append([]Reason{}, Reasons...), Reason{"trigger", "Started in a way Plumbix cannot run yet, such as tags, schedules or manual parameters."})

func knownReason(allowed []Reason, code string) bool {
	for _, r := range allowed {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Disabled reports whether the pipeline or any step is disabled; such a
// pipeline never runs.
func (p *Pipeline) Disabled() bool { return len(p.Blockers()) > 0 }

// Blockers counts the disabled steps per reason.
func (p *Pipeline) Blockers() map[string]int {
	b := map[string]int{}
	if p.Off != nil {
		b[p.Off.Reason]++
	}
	for _, st := range p.Steps {
		if st.Disabled != nil {
			b[st.Disabled.Reason]++
		}
	}
	return b
}

// BlockerSummary lists the blockers as "docker (2), task (1)", reasons sorted.
func (p *Pipeline) BlockerSummary() string {
	b := p.Blockers()
	codes := make([]string, 0, len(b))
	for c := range b {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = fmt.Sprintf("%s (%d)", c, b[c])
	}
	return strings.Join(parts, ", ")
}

func (ps *parser) disabled(p string, n *yaml.Node, allowed []Reason) *Disabled {
	if n.Kind != yaml.MappingNode {
		ps.add(p, n.Line, "must be a mapping with reason and an optional note")
		return nil
	}
	d := &Disabled{}
	var sawReason bool
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		fp := p + "." + key.Value
		switch key.Value {
		case "reason":
			sawReason = true
			if s, ok := ps.str(fp, val); ok {
				if !knownReason(allowed, s) {
					codes := make([]string, len(allowed))
					for j, r := range allowed {
						codes[j] = r.Code
					}
					ps.add(fp, val.Line, "must be one of %s", strings.Join(codes, ", "))
				}
				d.Reason = s
			}
		case "note":
			if s, ok := ps.str(fp, val); ok {
				if len(s) > MaxDisabledNote {
					ps.add(fp, val.Line, "must be at most 500 characters")
				}
				d.Note = s
			}
		default:
			ps.add(fp, key.Line, "unknown field")
		}
	}
	if !sawReason {
		ps.add(p+".reason", n.Line, "is required")
	}
	return d
}
