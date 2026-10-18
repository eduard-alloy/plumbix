package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/images"
)

// imageHandlers serve the Images page (WU-032, A32): what the server knows
// about the images its pipelines run, and builds of the shipped ones. A
// build is a person's act: these routes sit behind the session or token
// like every other, and the MCP server does not expose them.
type imageHandlers struct {
	images *images.Service
	svc    *change.Service
	engine *engine.Engine
	log    *slog.Logger
}

func registerImageRoutes(mux *http.ServeMux, h *imageHandlers) {
	mux.HandleFunc("GET /api/v1/images", h.list)
	mux.HandleFunc("POST /api/v1/images/{name}/build", h.build)
	mux.HandleFunc("GET /api/v1/images/builds/{id}", h.getBuild)
}

type imagesView struct {
	Images  []images.Info `json:"images"`
	Version string        `json:"version"` // the running server, which shipped images are compared with
}

// uses collects, per image reference, the steps of every registered
// repository's pipeline at its default branch that run it. A repository
// whose pipeline cannot be read contributes nothing: the page is about
// images, the Pipelines page says what is wrong with the file.
func (h *imageHandlers) uses(ctx context.Context) (map[string][]images.Use, error) {
	repos, err := h.svc.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]images.Use{}
	for _, repo := range repos {
		v, err := h.engine.PipelineFile(ctx, repo.ID, defaultBranch(repo))
		if err != nil || v.Pipeline == nil {
			continue
		}
		for _, st := range v.Pipeline.Steps {
			if st.Image == "" {
				continue // external validators run no container
			}
			out[st.Image] = append(out[st.Image], images.Use{RepoID: repo.ID, Repo: repoDisplayName(repo), Step: st.Name, Validator: st.Validator})
		}
	}
	return out, nil
}

func (h *imageHandlers) list(w http.ResponseWriter, r *http.Request) {
	uses, err := h.uses(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	list, err := h.images.List(r.Context(), uses)
	if err != nil {
		h.fail(w, err)
		return
	}
	if list == nil {
		list = []images.Info{}
	}
	writeJSON(w, http.StatusOK, imagesView{Images: list, Version: h.images.Version()})
}

func (h *imageHandlers) build(w http.ResponseWriter, r *http.Request) {
	b, err := h.images.Build(r.Context(), r.PathValue("name"))
	if err != nil {
		switch {
		case errors.Is(err, images.ErrNotShipped):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error()+": only the images Plumbix ships can be built here")
		case errors.Is(err, images.ErrBusy):
			writeError(w, http.StatusConflict, "busy", err.Error())
		default:
			h.fail(w, err)
		}
		return
	}
	h.log.Info("image build started", "image", b.Image, "build", b.ID)
	writeJSON(w, http.StatusAccepted, b)
}

func (h *imageHandlers) getBuild(w http.ResponseWriter, r *http.Request) {
	b, ok := h.images.GetBuild(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such build")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (h *imageHandlers) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, change.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	h.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// repoDisplayName is the last path element of the repository URL.
func repoDisplayName(repo change.Repo) string {
	u := strings.TrimSuffix(strings.TrimSuffix(repo.URL, "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		return u[i+1:]
	}
	return repo.ID
}
