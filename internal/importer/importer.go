// Package importer turns another CI system's pipelines into triggered
// Plumbix pipelines (WU-046, A36, A37). Each source system implements
// Source; every job is kept, and what Plumbix cannot run yet is disabled
// with a reason, so the pipeline stays disabled until it can run.
package importer

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// Dir is where imported pipelines are written.
const Dir = ".plumbix/pipelines/"

// Source is one CI system pipelines can be imported from.
type Source interface {
	// Name is the system's identifier, such as azure.
	Name() string
	// Detect reports whether a repository file is one of its pipelines.
	Detect(path string, content []byte) bool
	// Convert turns one pipeline file into a Plumbix pipeline. Name and
	// File of the result are set by Preview.
	Convert(path string, content []byte) (Converted, error)
}

// Converted is one source pipeline as a Plumbix pipeline.
type Converted struct {
	SourceFile string
	Name       string
	File       string
	Content    []byte
	// Steps summarises each step: its disabled reason and the source line.
	Steps []Step
	// Notes say what did not carry over, for the person importing.
	Notes []string
}

// Step is one converted step as the preview shows it.
type Step struct {
	Name   string
	Reason string // empty when the step runs
	Note   string
	Line   int
}

// Problem is a source file that could not be converted.
type Problem struct {
	SourceFile string
	Message    string
}

// Result is a preview over a repository's files.
type Result struct {
	Pipelines []Converted
	Problems  []Problem
}

// Preview converts every file src detects, in path order, naming each
// pipeline after its file and keeping names unique. A conversion that fails,
// or produces a file Plumbix cannot parse, is a problem.
func Preview(src Source, files map[string][]byte) Result {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var r Result
	taken := map[string]bool{}
	for _, p := range paths {
		if strings.HasPrefix(p, ".plumbix/") || !src.Detect(p, files[p]) {
			continue
		}
		c, err := src.Convert(p, files[p])
		if err != nil {
			r.Problems = append(r.Problems, Problem{SourceFile: p, Message: err.Error()})
			continue
		}
		if _, err := pipeline.ParseTriggered(c.Content); err != nil {
			r.Problems = append(r.Problems, Problem{SourceFile: p, Message: fmt.Sprintf("the converted pipeline does not parse: %v", err)})
			continue
		}
		base := PipelineName(p)
		name := base
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		taken[name] = true
		c.SourceFile, c.Name, c.File = p, name, Dir+name+".yaml"
		r.Pipelines = append(r.Pipelines, c)
	}
	return r
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// PipelineName names a pipeline after its source file: the base name
// without an azure-pipelines prefix or the extension, lowercased, with
// dashes for anything else; at most 63 characters.
func PipelineName(file string) string {
	base := strings.TrimSuffix(path.Base(file), path.Ext(file))
	lower := strings.ToLower(base)
	for _, prefix := range []string{"azure-pipelines-", "azure-pipelines."} {
		if rest, ok := strings.CutPrefix(lower, prefix); ok && rest != "" {
			lower = rest
			break
		}
	}
	return Slug(lower, "pipeline")
}

// Slug makes a Plumbix name: lowercase letters, digits and dashes, at most
// 63 characters, or fallback when nothing is left.
func Slug(s, fallback string) string {
	out := strings.Trim(nonName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" {
		return fallback
	}
	return out
}
