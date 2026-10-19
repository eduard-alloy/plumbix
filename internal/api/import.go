package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/importer"
	"github.com/eduard-alloy/plumbix/internal/importer/azure"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// importSources are the CI systems pipelines can be imported from (WU-046).
var importSources = map[string]importer.Source{"azure": azure.Source{}}

type importPreview struct {
	Branch    string            `json:"branch"`
	SHA       string            `json:"sha"`
	Source    string            `json:"source"`
	Pipelines []importedOne     `json:"pipelines"`
	Problems  []importedProblem `json:"problems"`
}

type importedOne struct {
	SourceFile string         `json:"source_file"`
	Name       string         `json:"name"`
	File       string         `json:"file"`
	Content    string         `json:"content"`
	Disabled   bool           `json:"disabled"`
	Blockers   map[string]int `json:"blockers"`
	Steps      []importedStep `json:"steps"`
	Notes      []string       `json:"notes"`
}

type importedStep struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
	Line   int    `json:"line"`
}

type importedProblem struct {
	SourceFile string `json:"source_file"`
	Message    string `json:"message"`
}

// importPreview is GET /repos/{id}/import: what importing the branch head's
// pipelines would write. It writes nothing.
func (h *pipelineHandlers) importPreview(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("source")
	src, ok := importSources[name]
	if !ok {
		known := make([]string, 0, len(importSources))
		for k := range importSources {
			known = append(known, k)
		}
		sort.Strings(known)
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: "source", Message: "must be one of " + strings.Join(known, ", ")}})
		return
	}
	repo, err := h.svc.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = defaultBranch(repo)
	}
	if !change.ValidBranch(branch) {
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: "branch", Message: "is not a valid branch name"}})
		return
	}
	v, err := h.engine.ImportPreview(r.Context(), repo.ID, branch, src)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := importPreview{Branch: v.Branch, SHA: v.SHA, Source: name, Pipelines: []importedOne{}, Problems: []importedProblem{}}
	for _, c := range v.Result.Pipelines {
		one := importedOne{SourceFile: c.SourceFile, Name: c.Name, File: c.File, Content: string(c.Content), Blockers: map[string]int{}, Steps: []importedStep{}, Notes: nonNilStrings(c.Notes)}
		if p, err := pipeline.ParseTriggered(c.Content); err == nil {
			one.Disabled, one.Blockers = p.Disabled(), p.Blockers()
		}
		for _, s := range c.Steps {
			one.Steps = append(one.Steps, importedStep{Name: s.Name, Reason: s.Reason, Note: s.Note, Line: s.Line})
		}
		out.Pipelines = append(out.Pipelines, one)
	}
	for _, p := range v.Result.Problems {
		out.Problems = append(out.Problems, importedProblem{SourceFile: p.SourceFile, Message: p.Message})
	}
	writeJSON(w, http.StatusOK, out)
}
