package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// importHandlers serve importing repositories from local folders (WU-041,
// A35). Every path is resolved inside root before git sees it.
type importHandlers struct {
	*changeHandlers
	root string
}

func registerImportRoutes(mux *http.ServeMux, h *importHandlers) {
	mux.HandleFunc("POST /api/v1/repos/inspect", h.inspect)
	mux.HandleFunc("POST /api/v1/repos/{id}/clone", h.clone)
}

// folderView is a local working tree with the registration it suggests.
type folderView struct {
	gitops.Folder
	Host    string `json:"host"`
	URL     string `json:"url"`
	Problem string `json:"problem"` // why the remote cannot be registered as it is; "" when it can
}

func (h *importHandlers) inspect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	path, err := inRoot(h.root, in.Path, true)
	if err != nil {
		writeProblems(w, "the path cannot be imported", []change.Problem{{Field: "path", Message: err.Error()}})
		return
	}
	f, err := gitops.InspectFolder(r.Context(), path)
	if err != nil {
		writeProblems(w, "the path cannot be imported", []change.Problem{{Field: "path", Message: err.Error()}})
		return
	}
	v := folderView{Folder: f}
	if f.Remote == "" {
		v.Problem = "the folder has no remote: publish it to a new repository online first"
	} else if host, url, err := change.DetectRemote(f.Remote); err != nil {
		v.Problem = err.Error()
	} else {
		v.Host, v.URL = host, url
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *importHandlers) clone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dest string `json:"dest"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if _, err := h.svc.GetRepo(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	dest, err := inRoot(h.root, in.Dest, false)
	if err != nil {
		writeProblems(w, "the destination cannot be used", []change.Problem{{Field: "dest", Message: err.Error()}})
		return
	}
	if err := h.engine.CloneRepo(r.Context(), id, dest); err != nil {
		if errors.Is(err, gitops.ErrNotEmpty) {
			writeProblems(w, "the destination cannot be used", []change.Problem{{Field: "dest", Message: err.Error()}})
			return
		}
		if errors.Is(err, change.ErrNotFound) {
			h.fail(w, err)
			return
		}
		writeError(w, http.StatusConflict, "clone_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": dest})
}

// inRoot resolves a path a person typed: "~" is root, anything else must be
// absolute; symlinks are resolved on the part that exists, and the result
// must lie in root. A path that must exist is resolved whole; one that may
// not exist yet needs an existing parent.
func inRoot(root, p string, mustExist bool) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", errors.New("is required")
	case p == "~":
		p = root
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(root, p[2:])
	case !filepath.IsAbs(p):
		return "", errors.New("must be absolute or start with ~/")
	}
	p = filepath.Clean(p)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("the import root is not readable")
	}
	var real string
	if mustExist {
		if real, err = filepath.EvalSymlinks(p); err != nil {
			return "", errors.New(p + " does not exist")
		}
	} else {
		parent, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			return "", errors.New(filepath.Dir(p) + " does not exist: create the parent folder first")
		}
		real = filepath.Join(parent, filepath.Base(p))
		if target, err := filepath.EvalSymlinks(real); err == nil {
			real = target
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", errors.New(p + " is outside " + root + ", the folder Plumbix may read and write")
	}
	return real, nil
}
