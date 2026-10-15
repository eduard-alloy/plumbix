package api

import (
	"errors"
	"net/http"
	"os"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/run"
)

type runHandlers struct {
	*changeHandlers
	engine *engine.Engine
	runs   run.Store
}

func registerRunRoutes(mux *http.ServeMux, h *runHandlers) {
	mux.HandleFunc("POST /api/v1/changes/{id}/validate", h.validate)
	mux.HandleFunc("GET /api/v1/changes/{id}/runs", h.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}", h.getRun)
	mux.HandleFunc("GET /api/v1/runs/{id}/steps/{step}/log", h.stepLog)
}

func (h *runHandlers) validate(w http.ResponseWriter, r *http.Request) {
	rn, err := h.engine.Validate(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, rn)
}

func (h *runHandlers) listRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.svc.Get(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	runs, err := h.runs.ListRuns(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *runHandlers) getRun(w http.ResponseWriter, r *http.Request) {
	rn, err := h.runs.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, change.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run "+r.PathValue("id")+" not found")
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rn)
}

// stepLog serves a step's log as plain text.
func (h *runHandlers) stepLog(w http.ResponseWriter, r *http.Request) {
	path := h.engine.LogPath(r.PathValue("id"), r.PathValue("step"))
	data, err := os.ReadFile(path)
	if path == "" || errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "not_found", "no log for that run and step")
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
