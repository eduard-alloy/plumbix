package api

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// approved submits a change on branch and waits for its approval.
func (a *runAPI) approved(branch string) change.Change {
	a.t.Helper()
	c := decode[change.Change](a.t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, branch)))
	first := decode[struct{ Runs []run.Run }](a.t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	if r := a.waitRun(first.ID); r.Outcome != change.Approved {
		a.t.Fatalf("run = %+v", r)
	}
	return c
}

func (a *runAPI) remoteMain() string {
	a.t.Helper()
	out, err := exec.Command("git", "-C", a.repo.Remote, "rev-parse", "main").Output()
	if err != nil {
		a.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestMergeEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	c := a.approved("feat")
	before := a.remoteMain()

	rec := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/merge", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Change", rec)
	merged := decode[change.Change](t, rec)
	if merged.State != change.Merged || merged.Merge == nil || merged.Merge.Previous != before || merged.Merge.Commit != a.remoteMain() {
		t.Fatalf("merged = %+v %+v, main %s", merged.State, merged.Merge, a.remoteMain())
	}
	o.check("Change", a.do(http.MethodGet, "/api/v1/changes/"+c.ID, ""))

	again := a.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/merge", "")
	o.check("Error", again)
	if again.Code != http.StatusConflict || decode[apiError](t, again).Error.Code != "invalid_state" {
		t.Errorf("merge twice: %d %s", again.Code, again.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/c_nope/merge", ""); rec.Code != http.StatusNotFound {
		t.Errorf("merge unknown: %d", rec.Code)
	}

	// A change whose target moved after validation is refused.
	a.repo.Commit("feat2", map[string]string{"b.go": "package b\n"}, "feature 2")
	c2 := a.approved("feat2")
	a.repo.Commit("main", map[string]string{"hot.go": "package hot\n"}, "hotfix")
	moved := a.do(http.MethodPost, "/api/v1/changes/"+c2.ID+"/merge", "")
	if moved.Code != http.StatusConflict || decode[apiError](t, moved).Error.Code != "target_moved" {
		t.Errorf("target moved: %d %s", moved.Code, moved.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes/"+c2.ID+"/validate", ""); rec.Code != http.StatusAccepted {
		t.Errorf("revalidate after a refused merge: %d", rec.Code)
	}
}

func TestAlertEndpoints(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	first, _, err := a.db.Raise(ctxBG, alert.Alert{Kind: alert.BypassCommit, Key: "main@abc", RepoID: a.repoID, Message: "foreign commit", Details: map[string]any{"branch": "main"}, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.db.Raise(ctxBG, alert.Alert{Kind: alert.TokenExpiry, Key: "github", Message: "expires soon", CreatedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	listRec := a.do(http.MethodGet, "/api/v1/alerts", "")
	o.check("AlertList", listRec)
	list := decode[struct{ Alerts []alert.Alert }](t, listRec).Alerts
	if len(list) != 2 || list[0].Kind != alert.TokenExpiry || list[1].ID != first.ID {
		t.Fatalf("alerts = %+v", list)
	}
	res := a.do(http.MethodPost, "/api/v1/alerts/"+first.ID+"/resolve", "")
	o.check("Alert", res)
	if res.Code != http.StatusOK || decode[alert.Alert](t, res).ResolvedAt == nil {
		t.Errorf("resolve: %d %s", res.Code, res.Body.String())
	}
	if open := decode[struct{ Alerts []alert.Alert }](t, a.do(http.MethodGet, "/api/v1/alerts", "")).Alerts; len(open) != 1 {
		t.Errorf("open after resolve = %+v", open)
	}
	if all := decode[struct{ Alerts []alert.Alert }](t, a.do(http.MethodGet, "/api/v1/alerts?include_resolved=true", "")).Alerts; len(all) != 2 {
		t.Errorf("all = %+v", all)
	}
	missing := a.do(http.MethodPost, "/api/v1/alerts/al_nope/resolve", "")
	o.check("Error", missing)
	if missing.Code != http.StatusNotFound {
		t.Errorf("resolve unknown: %d", missing.Code)
	}
}
