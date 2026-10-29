package api

import (
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// Criteria sets (WU-066, A43): acceptance criteria written with the
// specification, which a change or a pull request names instead of
// stating its own. Written through the API token or a browser session;
// a pull request can only name one.

func registerCriteriaSetRoutes(mux *http.ServeMux, h *changeHandlers) {
	mux.HandleFunc("POST /api/v1/criteria-sets", h.createCriteriaSet)
	mux.HandleFunc("GET /api/v1/criteria-sets", h.listCriteriaSets)
	mux.HandleFunc("GET /api/v1/criteria-sets/{id}", h.getCriteriaSet)
	mux.HandleFunc("PUT /api/v1/criteria-sets/{id}", h.updateCriteriaSet)
}

func (h *changeHandlers) createCriteriaSet(w http.ResponseWriter, r *http.Request) {
	var in change.CriteriaSetInput
	if !decodeJSON(w, r, &in) {
		return
	}
	cs, err := h.svc.CreateCriteriaSet(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("criteria set created", "set", cs.ID, "task", cs.Task, "owner", cs.Owner, "criteria", len(cs.Criteria))
	writeJSON(w, http.StatusCreated, cs)
}

func (h *changeHandlers) updateCriteriaSet(w http.ResponseWriter, r *http.Request) {
	var in change.CriteriaSetInput
	if !decodeJSON(w, r, &in) {
		return
	}
	cs, err := h.svc.UpdateCriteriaSet(r.Context(), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("criteria set replaced", "set", cs.ID, "criteria", len(cs.Criteria))
	writeJSON(w, http.StatusOK, cs)
}

func (h *changeHandlers) getCriteriaSet(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.GetCriteriaSet(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (h *changeHandlers) listCriteriaSets(w http.ResponseWriter, r *http.Request) {
	sets, err := h.svc.ListCriteriaSets(r.Context(), r.URL.Query().Get("task"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"criteria_sets": sets})
}
