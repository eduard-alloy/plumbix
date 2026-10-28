// Package gittest creates local git repositories for tests. Only test code
// imports it.
package gittest

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a bare "remote" repository plus a working clone used to commit.
type Repo struct {
	t      *testing.T
	Remote string // bare repository path, usable as a clone URL
	work   string
	home   string
}

// New creates a bare remote with one commit on main.
func New(t *testing.T) *Repo {
	t.Helper()
	return newRepo(t, "sha1")
}

// NewSHA256 is New for a repository with SHA-256 object ids (64-character
// hashes), as `git init --object-format=sha256` makes.
func NewSHA256(t *testing.T) *Repo {
	t.Helper()
	return newRepo(t, "sha256")
}

func newRepo(t *testing.T, objectFormat string) *Repo {
	t.Helper()
	root := t.TempDir()
	r := &Repo{t: t, Remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work"), home: t.TempDir()}
	r.run(root, "init", "-q", "--bare", "--initial-branch=main", "--object-format="+objectFormat, r.Remote)
	r.run(root, "clone", "-q", r.Remote, r.work)
	r.Commit("main", map[string]string{"README.md": "# test\n"}, "initial commit")
	return r
}

func (r *Repo) cmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	return c
}

func (r *Repo) run(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.cmd(dir, args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *Repo) exists(ref string) bool {
	return r.cmd(r.work, "rev-parse", "--verify", "-q", ref).Run() == nil
}

// Commit writes files on branch (an empty string deletes the file), pushes,
// and returns the new commit's SHA. A new branch starts from main.
func (r *Repo) Commit(branch string, files map[string]string, msg string) string {
	r.t.Helper()
	switch {
	case r.exists("origin/" + branch):
		r.run(r.work, "checkout", "-q", "-B", branch, "origin/"+branch)
	case r.exists("origin/main"):
		r.run(r.work, "checkout", "-q", "-B", branch, "origin/main")
	}
	for name, content := range files {
		if content == "" {
			r.run(r.work, "rm", "-q", name)
			continue
		}
		p := filepath.Join(r.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.run(r.work, "add", name)
	}
	r.run(r.work, "commit", "-q", "-m", msg)
	r.run(r.work, "push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch)
	r.run(r.work, "fetch", "-q", "origin")
	return r.run(r.work, "rev-parse", "HEAD")
}

// ServeHTTP serves the remote over git's smart HTTP protocol through
// git-http-backend, requiring basic auth with user and password. It returns
// the clone URL.
func (r *Repo) ServeHTTP(user, password string) string {
	r.t.Helper()
	execPath := r.run(r.work, "--exec-path")
	r.run(r.Remote, "config", "http.receivepack", "true")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(r.Remote), "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + user},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		u, p, ok := req.BasicAuth()
		if !ok || u != user || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, req)
	}))
	r.t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(r.Remote)
}
