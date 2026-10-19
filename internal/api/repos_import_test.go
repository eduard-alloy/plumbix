package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type folderBody struct {
	Path          string `json:"path"`
	Remote        string `json:"remote"`
	Host          string `json:"host"`
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
	HasPipeline   bool   `json:"has_pipeline"`
	Problem       string `json:"problem"`
}

func problemField(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Problems []struct{ Field, Message string } `json:"problems"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil || len(e.Error.Details.Problems) == 0 {
		t.Fatalf("not a problems body: %s", body)
	}
	return e.Error.Details.Problems[0].Field + ": " + e.Error.Details.Problems[0].Message
}

// TestInspectAFolderInsideTheImportRootOnly (WU-041): a working tree under
// the root is read; a path outside it, or a folder that is not a working
// tree, is refused naming the path.
func TestInspectAFolderInsideTheImportRootOnly(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	dir := filepath.Join(a.root, "dev", "app")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "clone", "-q", a.repo.Remote, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	rec := a.do(http.MethodPost, "/api/v1/repos/inspect", `{"path": "`+dir+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body.String())
	}
	o.check("RepoFolder", rec)
	var f folderBody
	_ = json.Unmarshal(rec.Body.Bytes(), &f)
	if f.Remote != a.repo.Remote || f.Host != "git" || f.URL != a.repo.Remote || f.DefaultBranch != "main" || !f.HasPipeline || f.Problem != "" || !strings.HasSuffix(f.Path, "/dev/app") {
		t.Errorf("folder = %+v", f)
	}
	// ~ is the import root.
	rec = a.do(http.MethodPost, "/api/v1/repos/inspect", `{"path": "~/dev/app"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("~ path: %d %s", rec.Code, rec.Body.String())
	}
	for name, path := range map[string]string{
		"outside the root": t.TempDir(),
		"dot-dot out":      filepath.Join(a.root, "..", filepath.Base(t.TempDir())),
		"relative":         "dev/app",
		"not a git tree":   a.root,
		"missing":          filepath.Join(a.root, "nope"),
	} {
		rec := a.do(http.MethodPost, "/api/v1/repos/inspect", `{"path": "`+path+`"}`)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(problemField(t, rec.Body.Bytes()), "path: ") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// A symlink inside the root that points outside it is outside.
	link := filepath.Join(a.root, "escape")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/inspect", `{"path": "`+link+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("symlink out of the root: %d %s", rec.Code, rec.Body.String())
	}
}

// TestCloneARegisteredRepository: a registered repository is cloned to a new
// folder under the root; a folder with files, a path outside the root and
// an unknown repository are refused.
func TestCloneARegisteredRepository(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	dest := filepath.Join(a.root, "dev", "plumbix")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/clone", `{"dest": "~/dev/plumbix"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body.String())
	}
	o.check("RepoClone", rec)
	if _, err := os.Stat(filepath.Join(dest, ".plumbix", "pipeline.yaml")); err != nil {
		t.Errorf("clone lacks the pipeline file: %v", err)
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/clone", `{"dest": "`+dest+`"}`); rec.Code != http.StatusBadRequest || !strings.Contains(problemField(t, rec.Body.Bytes()), "not empty") {
		t.Errorf("clone into a folder with files: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/clone", `{"dest": "`+filepath.Join(t.TempDir(), "x")+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("clone outside the root: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/clone", `{"dest": "~/no/such/parent/x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("clone under a missing parent: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos/r_unknown0000/clone", `{"dest": "~/dev/other"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repository: %d %s", rec.Code, rec.Body.String())
	}
}
