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

// TestGetRepoAndTombstone: GET /repos/{id} answers a live registration
// with the list's shape, a removed one as a 410 tombstone naming who
// removed it when, and an unknown id as 404; GET /repos?removed=true lists
// the tombstones while the plain list keeps hiding them (WU-067).
func TestGetRepoAndTombstone(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t)
	repo := a.createRepo()
	rec := a.do(http.MethodGet, "/api/v1/repos/"+repo.ID, "")
	o.check("Repo", rec)
	if rec.Code != http.StatusOK || decode[change.Repo](t, rec).URL != repo.URL {
		t.Errorf("live: %d %s", rec.Code, rec.Body.String())
	}
	unknown := a.do(http.MethodGet, "/api/v1/repos/r_nosuchrepo00", "")
	o.check("Error", unknown)
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown: %d %s", unknown.Code, unknown.Body.String())
	}
	if rec := a.do(http.MethodDelete, "/api/v1/repos/"+repo.ID, `{"by": "eduard@mac"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	gone := a.do(http.MethodGet, "/api/v1/repos/"+repo.ID, "")
	o.check("RepoTombstone", gone)
	ts := decode[change.RepoTombstone](t, gone)
	if gone.Code != http.StatusGone || ts.ID != repo.ID || ts.Host != repo.Host || ts.URL != repo.URL || !ts.Removed || ts.RemovedBy != "eduard@mac" || ts.RemovedAt.IsZero() {
		t.Errorf("tombstone: %d %s", gone.Code, gone.Body.String())
	}
	list := a.do(http.MethodGet, "/api/v1/repos?removed=true", "")
	o.check("RepoTombstoneList", list)
	if rs := decode[struct{ Repos []change.RepoTombstone }](t, list).Repos; list.Code != http.StatusOK || len(rs) != 1 || rs[0].ID != repo.ID || !rs[0].Removed {
		t.Errorf("removed listing: %d %s", list.Code, list.Body.String())
	}
	// The query is a boolean in any spelling; nonsense is refused.
	if rs := decode[struct{ Repos []change.RepoTombstone }](t, a.do(http.MethodGet, "/api/v1/repos?removed=1", "")).Repos; len(rs) != 1 {
		t.Errorf("removed=1: %+v", rs)
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos?removed=false", ""); rec.Code != http.StatusOK || len(decode[struct{ Repos []change.Repo }](t, rec).Repos) != 0 {
		t.Errorf("removed=false: %d %s", rec.Code, rec.Body.String())
	}
	bad := a.do(http.MethodGet, "/api/v1/repos?removed=maybe", "")
	o.check("Error", bad)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("removed=maybe: %d %s", bad.Code, bad.Body.String())
	}
	plain := a.do(http.MethodGet, "/api/v1/repos", "")
	o.check("RepoList", plain)
	if rs := decode[struct{ Repos []change.Repo }](t, plain).Repos; len(rs) != 0 {
		t.Errorf("plain listing after the removal: %+v", rs)
	}
	// A removed repository is still nothing to act on.
	if rec := a.do(http.MethodPut, "/api/v1/repos/"+repo.ID+"/settings", `{"paused": true}`); rec.Code != http.StatusNotFound {
		t.Errorf("settings of a removed repository: %d", rec.Code)
	}
}
