package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
)

const publishToken = "ghp_publish_SECRET"

// publishFixture is an import handler wired to a fake GitHub API; pushes to
// the created repository land in a local bare repository instead.
type publishFixture struct {
	t           *testing.T
	root        string
	mux         *http.ServeMux
	remote      string // where pushes land
	creates     []string
	handler     *importHandlers
	realAskpass func() (string, error)
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	f := &publishFixture{t: t, root: t.TempDir(), mux: http.NewServeMux()}
	f.remote = filepath.Join(t.TempDir(), "created.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+publishToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "eduard"})
		case "/user/repos":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.creates = append(f.creates, b["name"].(string))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "eduard/" + b["name"].(string)})
		}
	}))
	t.Cleanup(gh.Close)
	ap, err := gitops.EnsureAskpass(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &importHandlers{changeHandlers: &changeHandlers{log: slog.New(slog.NewTextHandler(io.Discard, nil))}, root: f.root,
		hosts:   host.Registry{"github": &host.GitHub{APIBase: gh.URL}, "azuredevops": &host.AzureDevOps{}},
		askpass: func() (string, error) { return ap, nil },
		pushURL: func(string) string { return f.remote }}
	registerImportRoutes(f.mux, h)
	f.handler, f.realAskpass = h, h.askpass
	return f
}

func (f *publishFixture) post(path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

// folder makes a working tree with commits on main and draft under the root.
func (f *publishFixture) folder(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.root, "dev", name)
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main", dir}, {"-C", dir, "commit", "-q", "--allow-empty", "-m", "first"}, {"-C", dir, "branch", "draft"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// TestPublishAFolderToANewGitHubRepository (WU-042): the repository is
// created with the given token, the folder gains origin and every branch is
// pushed; the answer names the URL to register.
func TestPublishAFolderToANewGitHubRepository(t *testing.T) {
	o := loadOpenAPI(t)
	f := newPublishFixture(t)
	f.folder("notes")
	rec := f.post("/api/v1/repos/publish", `{"path": "~/dev/notes", "host": "github", "owner": "", "name": "notes", "private": true, "token": "`+publishToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	o.check("RepoPublished", rec)
	var out struct {
		URL           string   `json:"url"`
		Host          string   `json:"host"`
		DefaultBranch string   `json:"default_branch"`
		Pushed        []string `json:"pushed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.URL != "https://github.com/eduard/notes" || out.Host != "github" || out.DefaultBranch != "main" || strings.Join(out.Pushed, ",") != "draft,main" {
		t.Errorf("published = %+v", out)
	}
	if strings.Contains(rec.Body.String(), publishToken) {
		t.Error("the answer echoes the token")
	}
	heads, _ := exec.Command("git", "-C", f.remote, "for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if strings.Join(strings.Fields(string(heads)), ",") != "draft,main" {
		t.Errorf("pushed branches = %q", heads)
	}
	// Publishing again: the folder now has a remote, nothing is created.
	rec = f.post("/api/v1/repos/publish", `{"path": "~/dev/notes", "host": "github", "name": "notes", "token": "`+publishToken+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(problemField(t, rec.Body.Bytes()), "already has a remote") || len(f.creates) != 1 {
		t.Errorf("second publish: %d %s, creates %v", rec.Code, rec.Body.String(), f.creates)
	}
}

func TestPublishRefusals(t *testing.T) {
	f := newPublishFixture(t)
	f.folder("app")
	for name, c := range map[string]struct {
		body  string
		field string
	}{
		"outside the root": {`{"path": "/etc", "host": "github", "name": "x"}`, "path"},
		"bad name":         {`{"path": "~/dev/app", "host": "github", "name": "no spaces"}`, "name"},
		"plain git":        {`{"path": "~/dev/app", "host": "git", "name": "app"}`, "host"},
		"azure no project": {`{"path": "~/dev/app", "host": "azuredevops", "owner": "acme", "name": "app"}`, "project"},
	} {
		rec := f.post("/api/v1/repos/publish", c.body)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(problemField(t, rec.Body.Bytes()), c.field+": ") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// A folder without commits is refused before anything is created online.
	empty := filepath.Join(f.root, "dev", "empty")
	if out, err := exec.Command("git", "init", "-q", empty).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	rec0 := f.post("/api/v1/repos/publish", `{"path": "~/dev/empty", "host": "github", "name": "empty", "token": "`+publishToken+`"}`)
	if rec0.Code != http.StatusBadRequest || !strings.Contains(problemField(t, rec0.Body.Bytes()), "no commits") || len(f.creates) != 0 {
		t.Errorf("empty folder: %d %s, creates %v", rec0.Code, rec0.Body.String(), f.creates)
	}
	// Credentials for the push are prepared before anything is created online (WU-043).
	f.handler.askpass = func() (string, error) { return "", errors.New("disk full") }
	rec1 := f.post("/api/v1/repos/publish", `{"path": "~/dev/app", "host": "github", "name": "app", "token": "`+publishToken+`"}`)
	if rec1.Code != http.StatusInternalServerError || len(f.creates) != 0 {
		t.Errorf("askpass failure: %d %s, creates %v", rec1.Code, rec1.Body.String(), f.creates)
	}
	if out, _ := exec.Command("git", "-C", filepath.Join(f.root, "dev", "app"), "remote").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("the folder gained a remote after an askpass failure: %q", out)
	}
	f.handler.askpass = f.realAskpass
	// A rejected token creates nothing and changes nothing in the folder.
	rec := f.post("/api/v1/repos/publish", `{"path": "~/dev/app", "host": "github", "name": "app", "token": "wrong"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "create_failed") || strings.Contains(rec.Body.String(), "wrong") {
		t.Errorf("rejected token: %d %s", rec.Code, rec.Body.String())
	}
	if out, _ := exec.Command("git", "-C", filepath.Join(f.root, "dev", "app"), "remote").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("the folder gained a remote after a failed create: %q", out)
	}
}
