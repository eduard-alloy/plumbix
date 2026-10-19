package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// triggeredList is GET /repos/{id}/pipelines (WU-045).
type triggeredList struct {
	Branch    string              `json:"branch"`
	SHA       string              `json:"sha"`
	Pipelines []triggeredPipeline `json:"pipelines"`
}

type triggeredPipeline struct {
	Name     string           `json:"name"`
	File     string           `json:"file"`
	On       *triggersBody    `json:"on"`
	Disabled bool             `json:"disabled"`
	Blockers map[string]int   `json:"blockers"`
	Problems []pipeline.Error `json:"problems"`
}

type triggersBody struct {
	Push   *pushBody `json:"push,omitempty"`
	Manual bool      `json:"manual"`
}

type pushBody struct {
	Branches []string `json:"branches"`
	Paths    []string `json:"paths"`
}

func (h *pipelineHandlers) listTriggered(w http.ResponseWriter, r *http.Request) {
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
	v, err := h.engine.TriggeredPipelines(r.Context(), repo.ID, branch)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := triggeredList{Branch: v.Branch, SHA: v.SHA, Pipelines: []triggeredPipeline{}}
	for _, f := range v.Pipelines {
		p := triggeredPipeline{Name: f.Name, File: f.File, Blockers: map[string]int{}, Problems: f.Problems}
		if p.Problems == nil {
			p.Problems = []pipeline.Error{}
		}
		if f.Pipeline != nil {
			p.Disabled, p.Blockers = f.Pipeline.Disabled(), f.Pipeline.Blockers()
			p.On = &triggersBody{Manual: f.Pipeline.On.Manual}
			if push := f.Pipeline.On.Push; push != nil {
				p.On.Push = &pushBody{Branches: nonNilStrings(push.Branches), Paths: nonNilStrings(push.Paths)}
			}
		}
		out.Pipelines = append(out.Pipelines, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// runTriggered is POST /repos/{id}/pipelines/{name}/runs: a manual run.
func (h *pipelineHandlers) runTriggered(w http.ResponseWriter, r *http.Request) {
	repo, err := h.svc.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "the body must be a JSON object with an optional branch")
		return
	}
	if body.Branch == "" {
		body.Branch = defaultBranch(repo)
	}
	if !change.ValidBranch(body.Branch) {
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: "branch", Message: "is not a valid branch name"}})
		return
	}
	run, err := h.engine.RunPipeline(r.Context(), repo.ID, r.PathValue("name"), body.Branch)
	var ve *pipeline.ValidationError
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, run)
	case errors.Is(err, engine.ErrNotManual):
		writeError(w, http.StatusConflict, "not_manual", err.Error())
	case errors.Is(err, engine.ErrPipelineDisabled):
		writeError(w, http.StatusConflict, "pipeline_disabled", err.Error())
	case errors.As(err, &ve):
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{Code: "pipeline_invalid", Message: "the pipeline file does not parse", Details: map[string]any{"problems": ve.Errors}}})
	default:
		h.fail(w, err)
	}
}
