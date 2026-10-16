package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// MaxJSONBytes is the largest JSON request body the API accepts.
const MaxJSONBytes = 1 << 20

type changeHandlers struct {
	svc    *change.Service
	engine *engine.Engine // nil when validation isn't wired
	log    *slog.Logger
}

func registerChangeRoutes(mux *http.ServeMux, h *changeHandlers) {
	mux.HandleFunc("POST /api/v1/repos", h.createRepo)
	mux.HandleFunc("GET /api/v1/repos", h.listRepos)
	mux.HandleFunc("POST /api/v1/changes", h.submit)
	mux.HandleFunc("GET /api/v1/changes", h.list)
	mux.HandleFunc("GET /api/v1/changes/{id}", h.get)
	mux.HandleFunc("POST /api/v1/changes/{id}/close", h.close)
}

func (h *changeHandlers) createRepo(w http.ResponseWriter, r *http.Request) {
	var in change.RepoInput
	if !decodeJSON(w, r, &in) {
		return
	}
	repo, err := h.svc.RegisterRepo(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (h *changeHandlers) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.svc.ListRepos(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

func (h *changeHandlers) submit(w http.ResponseWriter, r *http.Request) {
	var in change.ChangeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := h.svc.Submit(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	if h.engine != nil {
		if _, err := h.engine.Validate(r.Context(), c.ID); err != nil {
			h.log.Error("start validation", "change", c.ID, "err", err)
		} else if fresh, err := h.svc.Get(r.Context(), c.ID); err == nil {
			c = fresh
		}
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *changeHandlers) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	changes, err := h.svc.List(r.Context(), change.Filter{RepoID: q.Get("repo_id"), State: change.State(q.Get("state"))})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
}

func (h *changeHandlers) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *changeHandlers) close(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Close(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// fail maps service errors to HTTP responses.
func (h *changeHandlers) fail(w http.ResponseWriter, err error) {
	var inv *change.InvalidError
	switch {
	case errors.As(err, &inv):
		writeProblems(w, fmt.Sprintf("the request has %d problem(s)", len(inv.Problems)), inv.Problems)
	case errors.Is(err, change.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, change.ErrInvalidState):
		writeError(w, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, change.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, engine.ErrSourceMoved):
		writeError(w, http.StatusConflict, "source_moved", err.Error())
	case errors.Is(err, engine.ErrTargetMoved):
		writeError(w, http.StatusConflict, "target_moved", err.Error())
	case errors.Is(err, gitops.ErrPushRejected):
		writeError(w, http.StatusConflict, "push_rejected", err.Error())
	default:
		h.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// decodeJSON reads exactly one JSON object with no unknown fields into v.
// On failure it writes the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("body must contain a single JSON object")
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body is larger than 1 MiB")
	case errors.As(err, &typeErr):
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: typeErr.Field, Message: "must be a " + typeErr.Type.String()}})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: field, Message: "unknown field"}})
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON: "+err.Error())
	}
	return false
}
