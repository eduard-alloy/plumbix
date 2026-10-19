package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
)

// importHandlers serve importing repositories from local folders (WU-041,
// A35). Every path is resolved inside root before git sees it.
type importHandlers struct {
	*changeHandlers
	root string
	// hosts creates repositories online and supplies push credentials for
	// publishing (WU-042); askpass is the script git reads them through.
	hosts   host.Source
	askpass func() (string, error)
	// pushURL maps a created repository's URL to where the push goes; nil
	// pushes to the URL itself (tests point it at a local repository).
	pushURL func(string) string
}

func registerImportRoutes(mux *http.ServeMux, h *importHandlers) {
	mux.HandleFunc("POST /api/v1/repos/inspect", h.inspect)
	mux.HandleFunc("POST /api/v1/repos/{id}/clone", h.clone)
	mux.HandleFunc("POST /api/v1/repos/publish", h.publish)
}

var (
	repoNamePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9-]{0,39}$`)
	azureNamePattern   = regexp.MustCompile(`^[^/\\\s][^/\\]{0,63}$`)
)

// publish creates a repository online for a folder that has no remote and
// pushes the folder to it (WU-042, A35). Nothing changes before the host
// accepts the creation; a failed push says what exists.
func (h *importHandlers) publish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path    string `json:"path"`
		Host    string `json:"host"`
		Owner   string `json:"owner"`   // GitHub owner (empty: the token's account), or Azure DevOps organisation
		Project string `json:"project"` // Azure DevOps project
		Name    string `json:"name"`
		Private bool   `json:"private"`
		Token   string `json:"token"` // optional; the host token otherwise. Never echoed.
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var ps []change.Problem
	add := func(field, msg string) { ps = append(ps, change.Problem{Field: field, Message: msg}) }
	path, err := inRoot(h.root, in.Path, true)
	if err != nil {
		add("path", err.Error())
	}
	switch in.Host {
	case change.HostGitHub:
		if !githubOwnerPattern.MatchString(in.Owner) {
			add("owner", "must be a GitHub account or organisation name, or empty for the token's own account")
		}
	case change.HostAzureDevOps:
		if !azureNamePattern.MatchString(in.Owner) {
			add("owner", "must be the Azure DevOps organisation")
		}
		if !azureNamePattern.MatchString(in.Project) {
			add("project", "must be the Azure DevOps project")
		}
	default:
		add("host", "must be github or azuredevops: plain git has no API to create a repository")
	}
	if !repoNamePattern.MatchString(in.Name) {
		add("name", "letters, digits, dots, dashes and underscores, at most 100")
	}
	var folder gitops.Folder
	if path != "" {
		if folder, err = gitops.InspectFolder(r.Context(), path); err != nil {
			add("path", err.Error())
		} else if folder.Remote != "" {
			add("path", "already has a remote, "+folder.Remote+": import it from the Local folder tab")
		} else if branches, err := gitops.LocalBranches(r.Context(), path); err != nil || len(branches) == 0 {
			// Checked before the host creates anything, so an empty folder leaves nothing online.
			add("path", "has no commits to publish: commit something first")
		}
	}
	if len(ps) > 0 {
		writeProblems(w, "the folder cannot be published", ps)
		return
	}
	if h.hosts == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this server has no host credentials configured")
		return
	}
	// The push's credential script is ready before the host creates anything,
	// so this cannot fail with a repository already online.
	askpass, err := h.askpass()
	if err != nil {
		h.fail(w, err)
		return
	}
	reg := h.hosts.Hosts()
	var url string
	var auth gitops.Auth
	unavailable := func() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "this server has no "+in.Host+" host configured")
	}
	switch in.Host {
	case change.HostGitHub:
		g, ok := reg[change.HostGitHub].(*host.GitHub)
		if !ok {
			unavailable()
			return
		}
		url, err = g.CreateRepository(r.Context(), in.Token, in.Owner, in.Name, in.Private)
		auth, _ = g.AuthWith(in.Token)
	case change.HostAzureDevOps:
		a, ok := reg[change.HostAzureDevOps].(*host.AzureDevOps)
		if !ok {
			unavailable()
			return
		}
		url, err = a.CreateRepository(r.Context(), in.Token, in.Owner, in.Project, in.Name)
		auth, _ = a.AuthWith(in.Token)
	}
	// The token used may be the host's, not the request's: redact whichever it is.
	redact := func(s string) string { return redactSecret(redactSecret(s, in.Token), auth.Token) }
	if err != nil {
		writeError(w, http.StatusConflict, "create_failed", redact(err.Error()))
		return
	}
	target := url
	if h.pushURL != nil {
		target = h.pushURL(url)
	}
	pushed, err := gitops.Publish(r.Context(), path, target, &auth, askpass)
	if err != nil {
		writeError(w, http.StatusConflict, "push_failed", "created "+url+" online, but the push failed: "+redact(err.Error())+". Push the folder yourself, or delete the empty repository and try again.")
		return
	}
	h.log.Info("folder published", "url", url, "branches", len(pushed))
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "host": in.Host, "default_branch": folder.DefaultBranch, "pushed": pushed})
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
