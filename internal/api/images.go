package api

import (
	"context"
	"errors"
	"fmt"
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
	mux.HandleFunc("PUT /api/v1/images/own/{name}", h.putOwn)
	mux.HandleFunc("DELETE /api/v1/images/own/{name}", h.deleteOwn)
}

// putOwn creates or replaces an own image's Dockerfile (WU-033) and
// answers with the whole list, as the page shows it.
func (h *imageHandlers) putOwn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dockerfile string `json:"dockerfile"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	own, err := h.images.PutOwn(r.PathValue("name"), body.Dockerfile)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("own image saved", "image", own.Name)
	h.list(w, r)
}

// deleteOwn removes an own image's definition; a built image stays until
// Docker prunes it.
func (h *imageHandlers) deleteOwn(w http.ResponseWriter, r *http.Request) {
	if err := h.images.DeleteOwn(r.PathValue("name")); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("own image removed", "image", r.PathValue("name"))
	w.WriteHeader(http.StatusNoContent)
}

type imagesView struct {
	Images  []images.Info `json:"images"`
	Version string        `json:"version"` // the running server, which shipped images are compared with
}

// uses collects, per image reference, the steps of every registered
// repository's pipelines at its default branch that run it: the change
// pipeline and the triggered ones (WU-045). A pipeline that cannot be read
// contributes nothing: the page is about images, the Pipelines page says
// what is wrong with the file.
func (h *imageHandlers) uses(ctx context.Context) (map[string][]images.Use, error) {
	repos, err := h.svc.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]images.Use{}
	for _, repo := range repos {
		v, err := h.engine.PipelineFile(ctx, repo.ID, defaultBranch(repo))
		if err != nil {
			continue
		}
		if v.Pipeline != nil {
			for _, st := range v.Pipeline.Steps {
				if st.Image == "" {
					continue // external validators run no container
				}
				out[st.Image] = append(out[st.Image], images.Use{RepoID: repo.ID, Repo: repoDisplayName(repo), Step: st.Name, Validator: st.Validator})
			}
		}
		// PipelineFile just fetched: read the triggered pipelines at the
		// same head without fetching again.
		triggered, err := h.engine.TriggeredAt(ctx, repo.ID, v.SHA)
		if err != nil {
			h.log.Warn("read triggered pipelines for images", "repo", repo.ID, "err", err)
			continue
		}
		for _, f := range triggered {
			if f.Pipeline == nil {
				continue
			}
			for _, st := range f.Pipeline.Steps {
				if st.Image != "" {
					out[st.Image] = append(out[st.Image], images.Use{RepoID: repo.ID, Repo: repoDisplayName(repo), Step: st.Name, Pipeline: f.Name})
				}
			}
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
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error()+": only the images Plumbix ships, or own images defined on the Images page, can be built here")
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
	var inv *change.InvalidError
	if errors.As(err, &inv) {
		writeProblems(w, fmt.Sprintf("the request has %d problem(s)", len(inv.Problems)), inv.Problems)
		return
	}
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
