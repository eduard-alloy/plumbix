package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
)

type alertHandlers struct {
	alerts alert.Store
	log    *slog.Logger
}

func registerAlertRoutes(mux *http.ServeMux, h *alertHandlers) {
	mux.HandleFunc("GET /api/v1/alerts", h.list)
	mux.HandleFunc("POST /api/v1/alerts/{id}/resolve", h.resolve)
}

func (h *alertHandlers) list(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("include_resolved") == "true"
	alerts, err := h.alerts.List(r.Context(), all)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (h *alertHandlers) resolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.alerts.Resolve(r.Context(), id, time.Now().UTC()); err != nil {
		h.fail(w, err)
		return
	}
	a, err := h.alerts.Get(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *alertHandlers) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, change.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	h.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
