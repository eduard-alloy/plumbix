package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// pipelineHandlers serve a repository's pipeline file to the editor and
// turn an edited file into a change.
type pipelineHandlers struct {
	svc    *change.Service
	engine *engine.Engine
	runs   run.Store
	log    *slog.Logger
}

func registerPipelineRoutes(mux *http.ServeMux, h *pipelineHandlers) {
	mux.HandleFunc("GET /api/v1/repos/{id}/pipeline", h.get)
	mux.HandleFunc("POST /api/v1/repos/{id}/pipeline/changes", h.propose)
}

// pipelineFile is what the editor loads.
type pipelineFile struct {
	File     string           `json:"file"`
	Branch   string           `json:"branch"`
	SHA      string           `json:"sha"`
	Content  string           `json:"content"`
	Pipeline *pipelineModel   `json:"pipeline"`
	Problems []pipeline.Error `json:"problems"`
	LastRun  *lastRun         `json:"last_run,omitempty"`
}

// pipelineModel is the parsed file in the editor's shape.
type pipelineModel struct {
	Version int             `json:"version"`
	Steps   []pipelineStep  `json:"steps"`
	Stages  []pipelineStage `json:"stages"`
}

type pipelineStep struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Commands     []string          `json:"commands"`
	Needs        []string          `json:"needs"`
	Env          map[string]string `json:"env"`
	Secrets      []string          `json:"secrets"`
	Artifacts    []string          `json:"artifacts"`
	Timeout      string            `json:"timeout,omitempty"`
	Validator    bool              `json:"validator"`
	Retries      int               `json:"retries"`
	AllowFailure bool              `json:"allow_failure"`
	Paths        []string          `json:"paths"`
	Stage        string            `json:"stage,omitempty"`
}

type pipelineStage struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

// lastRun is the newest finished run on the branch, for card durations.
type lastRun struct {
	RunID string                 `json:"run_id"`
	Head  string                 `json:"head"`
	Steps map[string]lastRunStep `json:"steps"`
}

type lastRunStep struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

// proposalBody is what the editor posts to save.
type proposalBody struct {
	Branch       string `json:"branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Intent       string `json:"intent"`
	Content      string `json:"content"`
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toModel(p *pipeline.Pipeline) *pipelineModel {
	m := &pipelineModel{Version: p.Version, Steps: []pipelineStep{}, Stages: []pipelineStage{}}
	for _, st := range p.Steps {
		cmds := st.Commands
		if cmds == nil && st.Run != "" {
			cmds = []string{st.Run}
		}
		env := st.Env
		if env == nil {
			env = map[string]string{}
		}
		s := pipelineStep{Name: st.Name, Image: st.Image, Commands: nonNilStrings(cmds), Needs: nonNilStrings(st.Needs), Env: env,
			Secrets: nonNilStrings(st.Secrets), Artifacts: nonNilStrings(st.Artifacts), Validator: st.Validator, Retries: st.Retries,
			AllowFailure: st.AllowFailure, Paths: nonNilStrings(st.OnlyPaths), Stage: st.Stage}
		if st.Timeout > 0 {
			s.Timeout = st.Timeout.String()
		}
		m.Steps = append(m.Steps, s)
	}
	for _, sg := range p.Stages {
		m.Stages = append(m.Stages, pipelineStage{Name: sg.Name, Steps: nonNilStrings(sg.Steps)})
	}
	return m
}

// defaultBranch is the branch the editor opens: the first protected one.
func defaultBranch(repo change.Repo) string {
	if len(repo.ProtectedBranches) > 0 {
		return repo.ProtectedBranches[0]
	}
	return "main"
}

func (h *pipelineHandlers) get(w http.ResponseWriter, r *http.Request) {
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
	v, err := h.engine.PipelineFile(r.Context(), repo.ID, branch)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := pipelineFile{File: engine.PipelineFile, Branch: v.Branch, SHA: v.SHA, Content: string(v.Content), Problems: v.Problems}
	if out.Problems == nil {
		out.Problems = []pipeline.Error{}
	}
	if v.Pipeline != nil {
		out.Pipeline = toModel(v.Pipeline)
	}
	if last, err := h.lastRun(r.Context(), repo.ID, branch); err != nil {
		h.fail(w, err)
		return
	} else if last != nil {
		out.LastRun = last
	}
	writeJSON(w, http.StatusOK, out)
}

// lastRun finds the newest finished run on the branch.
func (h *pipelineHandlers) lastRun(ctx context.Context, repoID, branch string) (*lastRun, error) {
	runs, err := h.runs.ListRepoRuns(ctx, repoID, branch, 10)
	if err != nil {
		return nil, err
	}
	for _, rr := range runs {
		if rr.Status != run.Finished {
			continue
		}
		out := &lastRun{RunID: rr.ID, Head: rr.Head, Steps: map[string]lastRunStep{}}
		for _, s := range rr.Steps {
			out.Steps[s.Name] = lastRunStep{Status: s.Status, DurationMS: s.DurationMS}
		}
		return out, nil
	}
	return nil, nil
}

// pipelineCriteria are the acceptance criteria every proposed pipeline
// change carries. They have no deterministic check: the run itself and a
// person judge them, and a change to .plumbix/ always needs a person.
func pipelineCriteria() []change.Criterion {
	return []change.Criterion{
		{Text: "The pipeline file parses and its step graph has no cycles"},
		{Text: "Every step image can be pulled and every secret a step names exists"},
	}
}

func (h *pipelineHandlers) propose(w http.ResponseWriter, r *http.Request) {
	repo, err := h.svc.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var body proposalBody
	if !decodeJSON(w, r, &body) {
		return
	}
	var problems []change.Problem
	if body.Branch == "" || !change.ValidBranch(body.Branch) {
		problems = append(problems, change.Problem{Field: "branch", Message: "is required and must be a valid branch name"})
	}
	if body.TargetBranch == "" {
		body.TargetBranch = defaultBranch(repo)
	}
	if body.Title == "" {
		problems = append(problems, change.Problem{Field: "title", Message: "is required"})
	}
	if body.Content == "" {
		problems = append(problems, change.Problem{Field: "content", Message: "is required"})
	} else if _, err := pipeline.Parse([]byte(body.Content)); err != nil {
		var ve *pipeline.ValidationError
		if errors.As(err, &ve) {
			for _, e := range ve.Errors {
				where := "content"
				if e.Path != "" {
					where = "content." + e.Path
				}
				problems = append(problems, change.Problem{Field: where, Message: e.Message})
			}
		} else {
			problems = append(problems, change.Problem{Field: "content", Message: err.Error()})
		}
	}
	if len(problems) > 0 {
		writeProblems(w, fmt.Sprintf("the request has %d problem(s)", len(problems)), problems)
		return
	}
	if _, err := h.engine.ProposeFile(r.Context(), repo.ID, engine.Proposal{
		TargetBranch: body.TargetBranch, Branch: body.Branch, Path: engine.PipelineFile, Content: []byte(body.Content), Message: body.Title,
	}); err != nil {
		h.fail(w, err)
		return
	}
	c, err := h.svc.Submit(r.Context(), change.ChangeInput{
		RepoID: repo.ID, SourceBranch: body.Branch, TargetBranch: body.TargetBranch, Title: body.Title, Intent: body.Intent,
		AcceptanceCriteria: pipelineCriteria(), Constraints: []string{"only " + engine.PipelineFile + " changes"}, SubmittedBy: "pipeline editor",
	})
	if err != nil {
		h.fail(w, fmt.Errorf("branch %s was pushed but the change could not be opened: %w", body.Branch, err))
		return
	}
	if _, err := h.engine.Validate(r.Context(), c.ID); err != nil {
		h.log.Error("start validation", "change", c.ID, "err", err)
	} else if fresh, err := h.svc.Get(r.Context(), c.ID); err == nil {
		c = fresh
	}
	writeJSON(w, http.StatusCreated, c)
}

// fail maps errors to responses, the same way change handlers do, plus the
// proposal's own refusals.
func (h *pipelineHandlers) fail(w http.ResponseWriter, err error) {
	var inv *change.InvalidError
	switch {
	case errors.As(err, &inv):
		writeProblems(w, fmt.Sprintf("the request has %d problem(s)", len(inv.Problems)), inv.Problems)
	case errors.Is(err, change.ErrNotFound), errors.Is(err, gitops.ErrUnknownBranch):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, gitops.ErrBranchExists):
		writeError(w, http.StatusConflict, "branch_exists", err.Error())
	case errors.Is(err, gitops.ErrPushDenied):
		writeError(w, http.StatusConflict, "push_denied", err.Error())
	default:
		h.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
