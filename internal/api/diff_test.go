package api

import (
	"net/http"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/diff"
	"github.com/eduard-alloy/plumbix/internal/run"
)

func TestDiffEndpoint(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "feat")))
	first := decode[struct{ Runs []run.Run }](t, a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	r := a.waitRun(first.ID)

	rec := a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/diff", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Diff", rec)
	d := decode[diff.Diff](t, rec)
	if d.Base != r.Base || d.Head != r.Head || len(d.Files) != 1 || d.Files[0].Path != "a.go" || d.Files[0].Status != diff.Added || len(d.Files[0].Hunks) != 1 {
		t.Errorf("diff = %+v", d)
	}
	rec = a.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/diff?stat=true", "")
	o.check("Diff", rec)
	if s := decode[diff.Diff](t, rec); rec.Code != http.StatusOK || s.Additions != d.Additions || s.Files[0].Hunks != nil {
		t.Errorf("stat = %d %+v", rec.Code, s)
	}
	rec = a.do(http.MethodGet, "/api/v1/changes/c_nope/diff", "")
	o.check("Error", rec)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown change: %d", rec.Code)
	}
}
