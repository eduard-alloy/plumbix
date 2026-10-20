package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/importer"
	"github.com/eduard-alloy/plumbix/internal/importer/azure"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// importSources are the CI systems pipelines can be imported from (WU-046).
var importSources = map[string]importer.Source{"azure": azure.Source{}}

// importLabels name the systems for people.
var importLabels = map[string]string{"azure": "Azure Pipelines"}

type importPreview struct {
	Branch    string            `json:"branch"`
	SHA       string            `json:"sha"`
	Source    string            `json:"source"`
	Pipelines []importedOne     `json:"pipelines"`
	Problems  []importedProblem `json:"problems"`
	Files     []importedFile    `json:"files"`
}

// importedFile is one file of the plan (WU-048).
type importedFile struct {
	Path       string   `json:"path"`
	SourceFile string   `json:"source_file"`
	Status     string   `json:"status"`
	Content    string   `json:"content,omitempty"`
	Notes      []string `json:"notes"`
}

func planFiles(p importer.Plan) []importedFile {
	out := []importedFile{}
	for _, f := range p.Files {
		out = append(out, importedFile{Path: f.Path, SourceFile: f.SourceFile, Status: f.Status, Content: string(f.Content), Notes: nonNilStrings(f.Notes)})
	}
	return out
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
	File   string `json:"file"`
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
	src, ok := importSource(w, name)
	if !ok {
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
	out := importPreview{Branch: v.Branch, SHA: v.SHA, Source: name, Pipelines: []importedOne{}, Problems: []importedProblem{}, Files: planFiles(v.Plan)}
	for _, c := range v.Result.Pipelines {
		one := importedOne{SourceFile: c.SourceFile, Name: c.Name, File: c.File, Content: string(c.Content), Blockers: map[string]int{}, Steps: []importedStep{}, Notes: nonNilStrings(c.Notes)}
		if p, err := pipeline.ParseTriggered(c.Content); err == nil {
			one.Disabled, one.Blockers = p.Disabled(), p.Blockers()
		}
		for _, s := range c.Steps {
			one.Steps = append(one.Steps, importedStep{Name: s.Name, Reason: s.Reason, Note: s.Note, File: s.File, Line: s.Line})
		}
		out.Pipelines = append(out.Pipelines, one)
	}
	for _, p := range v.Result.Problems {
		out.Problems = append(out.Problems, importedProblem{SourceFile: p.SourceFile, Message: p.Message})
	}
	writeJSON(w, http.StatusOK, out)
}

// importSource finds a source system, answering 400 when there is none.
func importSource(w http.ResponseWriter, name string) (importer.Source, bool) {
	src, ok := importSources[name]
	if !ok {
		known := make([]string, 0, len(importSources))
		for k := range importSources {
			known = append(known, k)
		}
		sort.Strings(known)
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: "source", Message: "must be one of " + strings.Join(known, ", ")}})
	}
	return src, ok
}

type importBody struct {
	Source       string `json:"source"`
	TargetBranch string `json:"target_branch"`
	Branch       string `json:"branch"`
	Title        string `json:"title"`
}

type importChange struct {
	Change change.Change  `json:"change"`
	Files  []importedFile `json:"files"`
}

// importChange is POST /repos/{id}/import/changes (WU-048, A37): import or
// re-check the target branch's pipelines, commit the plan on a new branch
// and open a change for it. Touching .plumbix/, it needs a person.
func (h *pipelineHandlers) importChange(w http.ResponseWriter, r *http.Request) {
	repo, err := h.svc.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var body importBody
	if !decodeJSON(w, r, &body) {
		return
	}
	src, ok := importSource(w, body.Source)
	if !ok {
		return
	}
	if body.TargetBranch == "" {
		body.TargetBranch = defaultBranch(repo)
	}
	var problems []change.Problem
	if !change.ValidBranch(body.TargetBranch) {
		problems = append(problems, change.Problem{Field: "target_branch", Message: "is not a valid branch name"})
	}
	if body.Branch != "" && !change.ValidBranch(body.Branch) {
		problems = append(problems, change.Problem{Field: "branch", Message: "is not a valid branch name"})
	}
	if utf8.RuneCountInString(body.Title) > change.MaxTitle {
		problems = append(problems, change.Problem{Field: "title", Message: fmt.Sprintf("must be at most %d characters", change.MaxTitle)})
	}
	if len(problems) > 0 {
		writeProblems(w, fmt.Sprintf("the request has %d problem(s)", len(problems)), problems)
		return
	}
	label := importLabels[body.Source]
	title := body.Title
	if title == "" {
		title = "Import pipelines from " + label
	}
	p, err := h.engine.ImportPropose(r.Context(), repo.ID, engine.ImportRequest{TargetBranch: body.TargetBranch, Branch: body.Branch, Message: title}, src)
	if errors.Is(err, engine.ErrUpToDate) {
		writeError(w, http.StatusConflict, "up_to_date", err.Error())
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if body.Title == "" && !allAdded(p.Plan) {
		title = "Re-check pipelines imported from " + label
	}
	c, err := h.svc.Submit(r.Context(), change.ChangeInput{
		RepoID: repo.ID, SourceBranch: p.Branch, TargetBranch: body.TargetBranch, Title: title, Intent: importIntent(label, p),
		AcceptanceCriteria: append(pipelineCriteria(), change.Criterion{Text: "Every job of each source pipeline is kept; a job Plumbix cannot run is disabled with a reason, and so is its pipeline"}),
		Constraints:        []string{"only files under " + importer.Dir + " change"}, SubmittedBy: "pipeline import",
		PlumbixBranch: true,
	})
	if err != nil {
		h.fail(w, fmt.Errorf("branch %s was pushed but the change could not be opened: %w", p.Branch, err))
		return
	}
	if _, err := h.engine.Validate(r.Context(), c.ID); err != nil {
		h.log.Error("start validation", "change", c.ID, "err", err)
	} else if fresh, err := h.svc.Get(r.Context(), c.ID); err == nil {
		c = fresh
	}
	writeJSON(w, http.StatusCreated, importChange{Change: c, Files: planFiles(p.Plan)})
}

func allAdded(p importer.Plan) bool {
	for _, f := range p.Files {
		if f.Status != importer.StatusAdded {
			return false
		}
	}
	return true
}

// importIntent describes an import for the person approving it: what it
// writes per file, what it kept and why, and what could not be imported.
func importIntent(label string, p engine.ImportProposal) string {
	count := map[string]int{}
	for _, f := range p.Plan.Files {
		count[f.Status]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Plumbix imported the pipelines of %s at %s from %s: %d added, %d updated, %d removed.\n\n", p.ImportView.Branch, shortSHA(p.SHA), label, count[importer.StatusAdded], count[importer.StatusUpdated], count[importer.StatusRemoved])
	b.WriteString("Every job is kept. A job Plumbix cannot run yet is disabled with a reason, and a pipeline holding one does not run. What the import wrote is replaced on a re-check; what a person edited is kept. Once merged, a pipeline without a disabled step runs on its triggers: check the triggers, images and commands. Secrets reach them only on protected branches, once their files are approved.\n")
	head := b.String()

	var files []string
	for _, f := range p.Plan.Files {
		if f.Status == importer.StatusUnchanged && len(f.Notes) == 0 {
			continue
		}
		var line strings.Builder
		fmt.Fprintf(&line, "%s (%s, from %s)", f.Path, f.Status, f.SourceFile)
		if c := f.Content; c != nil {
			if pl, err := pipeline.ParseTriggered(c); err == nil {
				line.WriteString(": " + stepSummary(pl))
			}
		}
		line.WriteString(".")
		for _, n := range f.Notes {
			line.WriteString("\n   - " + n)
		}
		files = append(files, line.String())
	}
	var problems []string
	for _, pr := range p.Result.Problems {
		problems = append(problems, fmt.Sprintf("- %s: %s", pr.SourceFile, pr.Message))
	}
	for shown := len(files); ; shown-- {
		var b strings.Builder
		b.WriteString(head)
		if len(files) > 0 {
			b.WriteString("\nFiles:\n")
			for i, f := range files[:shown] {
				fmt.Fprintf(&b, "%d. %s\n", i+1, f)
			}
			if shown < len(files) {
				fmt.Fprintf(&b, "%d. and %d more files; see the diff.\n", shown+1, len(files)-shown)
			}
		}
		if len(problems) > 0 {
			b.WriteString("\nNot imported:\n" + strings.Join(problems, "\n") + "\n")
		}
		if out := strings.TrimRight(b.String(), "\n"); utf8.RuneCountInString(out) <= change.MaxIntent || shown == 0 {
			if utf8.RuneCountInString(out) > change.MaxIntent {
				out = string([]rune(out)[:change.MaxIntent])
			}
			return out
		}
	}
}

// stepSummary counts a pipeline's steps and what disables them.
func stepSummary(p *pipeline.Pipeline) string {
	s := fmt.Sprintf("%d steps", len(p.Steps))
	if !p.Disabled() {
		return s + ", runs"
	}
	blockers := p.Blockers()
	reasons := make([]string, 0, len(blockers))
	for r := range blockers {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, fmt.Sprintf("%s %d", r, blockers[r]))
	}
	return s + ", disabled: " + strings.Join(parts, ", ")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
