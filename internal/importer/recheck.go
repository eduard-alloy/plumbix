package importer

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// File statuses in a plan (WU-048).
const (
	StatusAdded     = "added"     // a new source pipeline: the file is written
	StatusUpdated   = "updated"   // the file changes
	StatusRemoved   = "removed"   // the source pipeline is gone and the file was untouched
	StatusUnchanged = "unchanged" // nothing to write
	StatusKept      = "kept"      // left as it is, for the reason in the notes
)

// Plan is what an import would write over the files already under Dir on
// the target branch (WU-048).
type Plan struct {
	Files []FilePlan
}

// FilePlan is one pipeline file of a plan.
type FilePlan struct {
	Path       string
	SourceFile string
	Status     string
	// Content is the file to write when added or updated; nil otherwise.
	Content []byte
	// Notes say what changed and what was kept, for the person approving.
	Notes []string
}

// Changed reports whether the plan writes anything.
func (p Plan) Changed() bool {
	for _, f := range p.Files {
		if f.Status == StatusAdded || f.Status == StatusUpdated || f.Status == StatusRemoved {
			return true
		}
	}
	return false
}

// Writes are the files the plan commits: content for added and updated
// files, nil for removed ones.
func (p Plan) Writes() map[string][]byte {
	out := map[string][]byte{}
	for _, f := range p.Files {
		switch f.Status {
		case StatusAdded, StatusUpdated:
			out[f.Path] = f.Content
		case StatusRemoved:
			out[f.Path] = nil
		}
	}
	return out
}

// Recheck plans an import of r over existing, the files under Dir on the
// target branch (WU-048, A37). A file is the import of a source pipeline
// when its top-level source names the same system and file. What the
// import wrote and nobody changed is replaced by the new conversion; what
// a person edited or added is kept, with a note when the source moved on.
func Recheck(system string, r Result, existing map[string][]byte) Plan {
	paths := make([]string, 0, len(existing))
	for p := range existing {
		if strings.HasPrefix(p, Dir) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	owned := map[string]string{} // source file -> path
	taken := map[string]bool{}
	for _, p := range paths {
		taken[p] = true
		_, root, err := parseDoc(existing[p])
		if err != nil {
			continue
		}
		src := get(root, "source")
		if file := scalar(get(src, "file")); scalar(get(src, "system")) == system && file != "" {
			if _, dup := owned[file]; !dup {
				owned[file] = p
			}
		}
	}

	var plan Plan
	seen := map[string]bool{}
	for _, c := range r.Pipelines {
		seen[c.SourceFile] = true
		if p, ok := owned[c.SourceFile]; ok {
			f := merge(existing[p], c.Content)
			f.Path, f.SourceFile = p, c.SourceFile
			plan.Files = append(plan.Files, f)
			continue
		}
		p := c.File
		for i := 2; taken[p]; i++ {
			p = fmt.Sprintf("%s%s-%d.yaml", Dir, c.Name, i)
		}
		taken[p] = true
		plan.Files = append(plan.Files, FilePlan{Path: p, SourceFile: c.SourceFile, Status: StatusAdded, Content: c.Content})
	}
	for _, pr := range r.Problems {
		seen[pr.SourceFile] = true
		if p, ok := owned[pr.SourceFile]; ok {
			plan.Files = append(plan.Files, FilePlan{Path: p, SourceFile: pr.SourceFile, Status: StatusKept,
				Notes: []string{fmt.Sprintf("%s cannot be imported now (%s); the file is left as it is", pr.SourceFile, pr.Message)}})
		}
	}
	gone := make([]string, 0)
	for file := range owned {
		if !seen[file] {
			gone = append(gone, file)
		}
	}
	sort.Strings(gone)
	for _, file := range gone {
		p := owned[file]
		f := FilePlan{Path: p, SourceFile: file, Status: StatusRemoved, Notes: []string{fmt.Sprintf("%s is gone from the source; the file is removed", file)}}
		if !untouched(existing[p]) {
			f.Status, f.Notes = StatusKept, []string{fmt.Sprintf("%s is gone from the source; the file is kept because it was edited", file)}
		}
		plan.Files = append(plan.Files, f)
	}
	sort.SliceStable(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	return plan
}

// untouched reports whether a file is entirely as the import wrote it.
func untouched(content []byte) bool {
	_, root, err := parseDoc(content)
	if err != nil || !topWritten(root) {
		return false
	}
	for _, st := range items(get(root, "steps")) {
		if !stepWritten(st) {
			return false
		}
	}
	return true
}

// piece is a step of the merged file and the file it came from.
type piece struct {
	node  *yaml.Node
	stage string
}

// merge plans one existing imported file against its new conversion.
func merge(old, fresh []byte) FilePlan {
	if bytes.Equal(old, fresh) {
		return FilePlan{Status: StatusUnchanged}
	}
	oldDoc, oldRoot, err := parseDoc(old)
	if err == nil {
		// Decoding catches what the node parser lets through, such as a
		// duplicate key.
		_, err = Fingerprint(oldRoot)
	}
	if err != nil {
		return FilePlan{Status: StatusKept, Notes: []string{fmt.Sprintf("the file is not valid YAML (%v); it is left as it is", err)}}
	}
	newDoc, newRoot, err := parseDoc(fresh)
	if err != nil {
		return FilePlan{Status: StatusKept, Notes: []string{"the new conversion does not parse; the file is left as it is"}}
	}
	var notes []string
	note := func(format string, a ...any) { notes = append(notes, fmt.Sprintf(format, a...)) }

	topOK := topWritten(oldRoot)
	baseDoc, baseRoot := newDoc, newRoot
	if !topOK {
		baseDoc, baseRoot = oldDoc, oldRoot
		if a, _ := Fingerprint(oldRoot, pipelineSkip...); a != scalar(get(get(newRoot, "source"), "written")) {
			note("pipeline settings: kept as you edited them; the import would now write different ones")
		}
	}

	oldStage, newStage := stagesOf(oldRoot), stagesOf(newRoot)
	oldSteps := items(get(oldRoot, "steps"))
	oldByName := map[string]*yaml.Node{}
	for _, st := range oldSteps {
		if n := scalar(get(st, "name")); n != "" {
			oldByName[n] = st
		}
	}
	allOwned := topOK
	used := map[string]bool{}
	var out []piece
	for _, ns := range items(get(newRoot, "steps")) {
		name := scalar(get(ns, "name"))
		os := oldByName[name]
		switch {
		case os == nil:
			out = append(out, piece{ns, newStage[name]})
			note("%s: added, a new job in the source", name)
		case get(os, "source") == nil:
			allOwned = false
			out = append(out, piece{os, oldStage[name]})
			note("%s: the source has a job of this name; your step is kept and the job is not added", name)
		case stepWritten(os):
			out = append(out, piece{ns, newStage[name]})
			if s := changeNote(os, ns); s != "" {
				note("%s: %s", name, s)
			}
		default:
			allOwned = false
			out = append(out, piece{os, oldStage[name]})
			if hashOf := func(n *yaml.Node) string { return scalar(get(get(n, "source"), "hash")) }; hashOf(os) != hashOf(ns) {
				note("%s: kept as you edited it; its source job changed since the import", name)
			}
		}
		used[name] = true
	}
	var dropped []piece
	for _, os := range oldSteps {
		name := scalar(get(os, "name"))
		if used[name] {
			continue
		}
		switch {
		case get(os, "source") == nil:
			allOwned = false
			out = append(out, piece{os, oldStage[name]})
		case stepWritten(os):
			dropped = append(dropped, piece{os, oldStage[name]})
		default:
			allOwned = false
			out = append(out, piece{os, oldStage[name]})
			note("%s: kept as you edited it; its job is gone from the source", name)
		}
	}
	// A step a kept step needs stays, even when its job is gone.
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(dropped); i++ {
			name := scalar(get(dropped[i].node, "name"))
			if by := neededBy(out, name); by != "" {
				allOwned = false
				out = append(out, dropped[i])
				note("%s: its job is gone from the source; kept because %s needs it", name, by)
				dropped = append(dropped[:i], dropped[i+1:]...)
				changed = true
				break
			}
		}
	}
	for _, d := range dropped {
		note("%s: removed, its job is gone from the source", scalar(get(d.node, "name")))
	}

	content := fresh
	if !allOwned {
		root := &yaml.Node{Kind: yaml.MappingNode}
		for i := 0; i+1 < len(baseRoot.Content); i += 2 {
			switch baseRoot.Content[i].Value {
			case "steps", "stages":
				continue
			}
			root.Content = append(root.Content, baseRoot.Content[i], baseRoot.Content[i+1])
		}
		steps := &yaml.Node{Kind: yaml.SequenceNode}
		for _, p := range out {
			steps.Content = append(steps.Content, p.node)
		}
		set(root, "steps", steps)
		if st := stagesNode(out, stageOrder(newRoot, oldRoot)); st != nil {
			set(root, "stages", st)
		}
		doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: baseDoc.HeadComment, FootComment: baseDoc.FootComment, Content: []*yaml.Node{root}}
		b, err := encode(doc)
		if err != nil {
			return FilePlan{Status: StatusKept, Notes: append(notes, "the merged file could not be written ("+err.Error()+"); the file is left as it is")}
		}
		content = b
	}
	if _, err := pipeline.ParseTriggered(content); err != nil {
		return FilePlan{Status: StatusKept, Notes: append(notes, "the merged file would not parse ("+err.Error()+"); the file is left as it is")}
	}
	if same(old, content) {
		return FilePlan{Status: StatusUnchanged, Notes: notes}
	}
	return FilePlan{Status: StatusUpdated, Content: content, Notes: notes}
}

// changeNote says how an untouched step's new conversion differs.
func changeNote(old, fresh *yaml.Node) string {
	reason := func(n *yaml.Node) string { return scalar(get(get(n, "disabled"), "reason")) }
	hash := func(n *yaml.Node) string { return scalar(get(get(n, "source"), "hash")) }
	a, _ := Fingerprint(old, "source")
	b, _ := Fingerprint(fresh, "source")
	was, now := reason(old), reason(fresh)
	switch {
	case a == b && hash(old) == hash(fresh):
		return ""
	case was != "" && now == "":
		return "now runs (was disabled: " + was + ")"
	case was == "" && now != "":
		return "now disabled: " + now
	case was != now:
		return "still disabled, now for " + now + " (was " + was + ")"
	case hash(old) != hash(fresh):
		return "updated, its source job changed"
	}
	return "converted differently by this release"
}

// neededBy names a step in out that needs name, or empty.
func neededBy(out []piece, name string) string {
	for _, p := range out {
		for _, n := range items(get(p.node, "needs")) {
			if n.Value == name {
				return scalar(get(p.node, "name"))
			}
		}
	}
	return ""
}

// stagesOf maps each step to its stage.
func stagesOf(root *yaml.Node) map[string]string {
	out := map[string]string{}
	st := get(root, "stages")
	if st == nil || st.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(st.Content); i += 2 {
		for _, n := range items(st.Content[i+1]) {
			out[n.Value] = st.Content[i].Value
		}
	}
	return out
}

// stageOrder lists stage names, the new conversion's first.
func stageOrder(roots ...*yaml.Node) []string {
	var order []string
	seen := map[string]bool{}
	for _, r := range roots {
		st := get(r, "stages")
		if st == nil || st.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(st.Content); i += 2 {
			if name := st.Content[i].Value; !seen[name] {
				seen[name] = true
				order = append(order, name)
			}
		}
	}
	return order
}

// stagesNode rebuilds stages from where each step came from.
func stagesNode(out []piece, order []string) *yaml.Node {
	members := map[string][]string{}
	for _, p := range out {
		if p.stage != "" {
			members[p.stage] = append(members[p.stage], scalar(get(p.node, "name")))
		}
	}
	st := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range order {
		if len(members[name]) == 0 {
			continue
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range members[name] {
			seq.Content = append(seq.Content, str(s))
		}
		st.Content = append(st.Content, str(name), seq)
	}
	if len(st.Content) == 0 {
		return nil
	}
	return st
}

// same reports whether two files have the same parsed content.
func same(a, b []byte) bool {
	_, ra, err1 := parseDoc(a)
	_, rb, err2 := parseDoc(b)
	if err1 != nil || err2 != nil {
		return false
	}
	x, err1 := Fingerprint(ra)
	y, err2 := Fingerprint(rb)
	return err1 == nil && err2 == nil && x == y
}
