package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestRepoPauseAndRemove: paused is a repository setting; DELETE retires a
// repository at a named person's request, refused with its own code
// while a change is open or running (WU-065).
func TestRepoPauseAndRemove(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t) // the settings routes carry paused
	repo := a.createRepo()
	rec := a.do(http.MethodPut, "/api/v1/repos/"+repo.ID+"/settings", `{"paused": true}`)
	o.check("RepoSettings", rec)
	if rec.Code != http.StatusOK || !decode[struct{ Paused bool }](t, rec).Paused {
		t.Errorf("pause: %d %s", rec.Code, rec.Body.String())
	}
	if rs := decode[struct{ Repos []change.Repo }](t, a.do(http.MethodGet, "/api/v1/repos", "")).Repos; len(rs) != 1 || !rs[0].Paused {
		t.Errorf("repos after the pause: %+v", rs)
	}
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(repo.ID, "feat")))
	busy := a.do(http.MethodDelete, "/api/v1/repos/"+repo.ID, `{"by": "eduard@mac"}`)
	o.check("Error", busy)
	if busy.Code != http.StatusConflict || decode[apiError](t, busy).Error.Code != "repo_busy" {
		t.Errorf("remove with an open change: %d %s", busy.Code, busy.Body.String())
	}
	noOne := a.do(http.MethodDelete, "/api/v1/repos/"+repo.ID, `{}`)
	o.check("Error", noOne)
	if noOne.Code != http.StatusBadRequest || !strings.Contains(noOne.Body.String(), `"by"`) {
		t.Errorf("remove without a person: %d %s", noOne.Code, noOne.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/close", ""); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodDelete, "/api/v1/repos/"+repo.ID, `{"by": "eduard@mac"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if rs := decode[struct{ Repos []change.Repo }](t, a.do(http.MethodGet, "/api/v1/repos", "")).Repos; len(rs) != 0 {
		t.Errorf("repos after the removal: %+v", rs)
	}
	if rec := a.do(http.MethodGet, "/api/v1/changes/"+c.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("the change of a removed repository: %d", rec.Code)
	}
	if rec := a.do(http.MethodDelete, "/api/v1/repos/"+repo.ID, `{"by": "eduard@mac"}`); rec.Code != http.StatusNotFound {
		t.Errorf("remove twice: %d", rec.Code)
	}
}
