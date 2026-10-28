package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/store"
)

// testAPI is an API handler backed by a fresh database.
type testAPI struct {
	t *testing.T
	h http.Handler
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &testAPI{t: t, h: NewHandler(Deps{Version: "dev", Token: testToken, Changes: change.NewService(db)})}
}

func (a *testAPI) do(method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	return v
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Problems []change.Problem `json:"problems"`
		} `json:"details"`
	} `json:"error"`
}

const repoBody = `{"host": "github", "url": "https://github.com/eduard-alloy/plumbix"}`

func (a *testAPI) createRepo() change.Repo {
	a.t.Helper()
	rec := a.do(http.MethodPost, "/api/v1/repos", repoBody)
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("create repo: %d %s", rec.Code, rec.Body.String())
	}
	return decode[change.Repo](a.t, rec)
}

func changeBody(repoID, branch string) string {
	b, _ := json.Marshal(map[string]any{
		"repo_id": repoID, "source_branch": branch, "target_branch": "main",
		"title": "Add retries", "intent": "Retry flaky uploads",
		"acceptance_criteria": []map[string]any{{"text": "Uploads retry", "check": map[string]any{"steps": []string{"test"}}}},
	})
	return string(b)
}

func TestRepoEndpoints(t *testing.T) {
	a := newTestAPI(t)
	r := a.createRepo()
	if r.ID == "" || r.MergeStrategy != "squash" {
		t.Errorf("repo = %+v", r)
	}
	if rec := a.do(http.MethodPost, "/api/v1/repos", repoBody); rec.Code != http.StatusConflict || decode[apiError](t, rec).Error.Code != "conflict" {
		t.Errorf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	list := decode[struct{ Repos []change.Repo }](t, a.do(http.MethodGet, "/api/v1/repos", ""))
	if len(list.Repos) != 1 {
		t.Errorf("list = %+v", list)
	}
}

func TestSubmitAndReadChange(t *testing.T) {
	a := newTestAPI(t)
	r := a.createRepo()
	rec := a.do(http.MethodPost, "/api/v1/changes", changeBody(r.ID, "feat"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	c := decode[change.Change](t, rec)
	if c.State != change.Open || c.AcceptanceCriteria[0].ID != "AC-1" {
		t.Errorf("change = %+v", c)
	}
	got := decode[change.Change](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID, ""))
	if got.ID != c.ID {
		t.Errorf("show = %+v", got)
	}
	if rec := a.do(http.MethodGet, "/api/v1/changes/c_nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing change: %d", rec.Code)
	}
}

func TestSubmitErrors(t *testing.T) {
	a := newTestAPI(t)
	cases := []struct {
		name, body string
		status     int
		code       string
		field      string
	}{
		{"not json", `{"repo_id":`, 400, "invalid_request", ""},
		{"unknown field", `{"repo_id": "x", "colour": "red"}`, 400, "invalid_request", "colour"},
		// plumbix_branch is the server's to set (WU-022): a client claiming it is refused, not ignored.
		{"plumbix_branch claimed", `{"repo_id": "x", "plumbix_branch": true}`, 400, "invalid_request", "plumbix_branch"},
		{"wrong type", `{"title": 5}`, 400, "invalid_request", "title"},
		{"two objects", `{} {}`, 400, "invalid_request", ""},
		{"rules", `{}`, 400, "invalid_request", "acceptance_criteria"},
		{"unknown repo", changeBody("r_nope", "feat"), 404, "not_found", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := a.do(http.MethodPost, "/api/v1/changes", tc.body)
			e := decode[apiError](t, rec)
			if rec.Code != tc.status || e.Error.Code != tc.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
			if tc.field == "" {
				return
			}
			for _, p := range e.Error.Details.Problems {
				if p.Field == tc.field {
					return
				}
			}
			t.Errorf("problems %+v, want one for %s", e.Error.Details.Problems, tc.field)
		})
	}
	big := `{"title": "` + strings.Repeat("x", 1<<20) + `"}`
	if rec := a.do(http.MethodPost, "/api/v1/changes", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
}

func TestConflictCloseAndList(t *testing.T) {
	a := newTestAPI(t)
	r := a.createRepo()
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(r.ID, "feat")))
	if rec := a.do(http.MethodPost, "/api/v1/changes", changeBody(r.ID, "feat")); rec.Code != http.StatusConflict {
		t.Errorf("second open change: %d", rec.Code)
	}
	rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/close", "")
	if rec.Code != http.StatusOK || decode[change.Change](t, rec).State != change.Closed {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/close", ""); rec.Code != http.StatusConflict || decode[apiError](t, rec).Error.Code != "invalid_state" {
		t.Errorf("close twice: %d %s", rec.Code, rec.Body.String())
	}
	_ = decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(r.ID, "feat")))
	list := decode[struct{ Changes []change.Change }](t, a.do(http.MethodGet, "/api/v1/changes?state=open&repo_id="+r.ID, ""))
	if len(list.Changes) != 1 || list.Changes[0].State != change.Open {
		t.Errorf("open list = %+v", list)
	}
	if rec := a.do(http.MethodGet, "/api/v1/changes?state=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad state filter: %d", rec.Code)
	}
	all := decode[struct{ Changes []change.Change }](t, a.do(http.MethodGet, "/api/v1/changes", ""))
	if len(all.Changes) != 2 {
		t.Errorf("all = %d changes, want 2", len(all.Changes))
	}
}

func TestChangeRoutesNeedToken(t *testing.T) {
	a := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/changes", nil)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestApproveEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("pipe", map[string]string{".plumbix/pipeline.yaml": "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: lint, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test, lint]}\n"}, "pipeline edit")
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "pipe")))
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	if r := a.waitRun(first.ID); r.Outcome != change.NeedsHuman {
		t.Fatalf("run = %+v", r)
	}
	bad := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/approve", `{"reason": " "}`)
	o.check("Error", bad)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), `"reason"`) {
		t.Errorf("empty reason: %d %s", bad.Code, bad.Body.String())
	}
	rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/approve", `{"reason": "pipeline edit reviewed by hand"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Change", rec)
	got := decode[change.Change](t, rec)
	if got.State != change.Approved || got.HumanApproval == nil || got.HumanApproval.Reason != "pipeline edit reviewed by hand" || got.HumanApproval.Head == "" {
		t.Errorf("approved = %+v %+v", got.State, got.HumanApproval)
	}
	again := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/approve", `{"reason": "again"}`)
	o.check("Error", again)
	if again.Code != http.StatusConflict || decode[apiError](t, again).Error.Code != "invalid_state" {
		t.Errorf("approve twice: %d %s", again.Code, again.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/c_nope/approve", `{"reason": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
	merged := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/merge", "")
	if merged.Code != http.StatusOK || decode[change.Change](t, merged).State != change.Merged {
		t.Errorf("merge after approval: %d %s", merged.Code, merged.Body.String())
	}
}

func TestRefreshEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	content := "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: lint, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test, lint]}\n"
	body := `{"branch": "pipeline/add-lint", "title": "Pipeline: add lint", "intent": "Lint every change.", "content": ` + jsonString(content) + `}`
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/repos/"+a.repoID+"/pipeline/changes", body))
	if !c.PlumbixBranch {
		t.Fatalf("the editor's change does not carry plumbix_branch: %+v", c)
	}
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	if r := a.waitRun(first.ID); r.Outcome != change.NeedsHuman {
		t.Fatalf("run = %+v", r)
	}
	cur := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/refresh", "")
	o.check("Error", cur)
	if cur.Code != http.StatusConflict || decode[apiError](t, cur).Error.Code != "already_current" {
		t.Errorf("refresh while current: %d %s", cur.Code, cur.Body.String())
	}
	a.repo.Commit("main", map[string]string{"x.txt": "x\n"}, "main moves")
	rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/refresh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Change", rec)
	if got := decode[change.Change](t, rec); got.State != change.Running || !got.PlumbixBranch {
		t.Errorf("refreshed change = %s %v", got.State, got.PlumbixBranch)
	}
	runs := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs
	if r := a.waitRun(runs[len(runs)-1].ID); r.Iteration != 2 || r.Base != a.remoteMain() || r.Outcome != change.NeedsHuman {
		t.Errorf("new run = %+v", r)
	}
	agent := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat")))
	a.waitRun(decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+agent.ID+"/runs", "")).Runs[0].ID)
	theirs := a.do(http.MethodPost, "/api/v1/changes/"+agent.ID+"/refresh", "")
	o.check("Error", theirs)
	if theirs.Code != http.StatusConflict || decode[apiError](t, theirs).Error.Code != "not_plumbix_branch" {
		t.Errorf("refresh of an agent's branch: %d %s", theirs.Code, theirs.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/c_nope/refresh", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
}

// TestRepoWithPullRequests: a repository that gates pull requests reads
// back with GitHub as its merger, and both shapes match the contract.
func TestRepoWithPullRequests(t *testing.T) {
	o := loadOpenAPI(t)
	a := newTestAPI(t)
	rec := a.do(http.MethodPost, "/api/v1/repos", `{"host": "github", "url": "https://github.com/a/gated", "pull_requests": true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Repo", rec)
	r := decode[change.Repo](t, rec)
	if !r.PullRequests || r.Merger != change.MergerGitHub {
		t.Errorf("repo = %+v", r)
	}
	plain := a.createRepo()
	if plain.PullRequests || plain.Merger != change.MergerPlumbix {
		t.Errorf("plain repo = %+v", plain)
	}
	bad := a.do(http.MethodPost, "/api/v1/repos", `{"host": "azuredevops", "url": "https://dev.azure.com/o/p/_git/r", "pull_requests": true}`)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("pull requests on Azure DevOps: %d %s", bad.Code, bad.Body.String())
	}
	o.check("RepoList", a.do(http.MethodGet, "/api/v1/repos", ""))
}
