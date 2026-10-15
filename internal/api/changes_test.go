package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
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
