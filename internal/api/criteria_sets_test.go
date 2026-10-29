package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestCriteriaSetsOverTheAPI: a set is created, listed, read and replaced
// through the API; a submission naming it takes its criteria and records
// the set; a used set is refused on edit with its own code; an unknown set
// is 404 (WU-066).
func TestCriteriaSetsOverTheAPI(t *testing.T) {
	o := loadOpenAPI(t)
	a := newTestAPI(t)
	repo := a.createRepo()
	body := `{"task": "kai:plumbix/X6Q5JY", "title": "WU-066 Criteria sets", "owner": "eduard@madmax.local", "source": {"kind": "kai", "project": "plumbix", "ref": "X6Q5JY"}, "criteria": [{"text": "A criteria set is created, listed and read through the API and the CLI"}, {"text": "The build passes", "check": {"steps": ["build", "test"]}}]}`
	rec := a.do(http.MethodPost, "/api/v1/criteria-sets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	o.check("CriteriaSet", rec)
	cs := decode[change.CriteriaSet](t, rec)
	if !strings.HasPrefix(cs.ID, "cs_") || len(cs.Criteria) != 2 || cs.Criteria[1].Check == nil || cs.Owner != "eduard@madmax.local" || len(cs.UsedBy) != 0 {
		t.Fatalf("set = %+v", cs)
	}
	bad := a.do(http.MethodPost, "/api/v1/criteria-sets", `{"task": "x", "owner": "me", "criteria": [{"text": "x", "check": {"command": "ls"}}]}`)
	o.check("Error", bad)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "command") {
		t.Errorf("a command in a set: %d %s", bad.Code, bad.Body.String())
	}
	rec = a.do(http.MethodGet, "/api/v1/criteria-sets?task=kai:plumbix/X6Q5JY", "")
	o.check("CriteriaSetList", rec)
	if list := decode[struct {
		Sets []change.CriteriaSet `json:"criteria_sets"`
	}](t, rec).Sets; len(list) != 1 || list[0].ID != cs.ID {
		t.Errorf("list = %+v", list)
	}
	rec = a.do(http.MethodGet, "/api/v1/criteria-sets/"+cs.ID, "")
	o.check("CriteriaSet", rec)
	if rec.Code != http.StatusOK {
		t.Errorf("get: %d", rec.Code)
	}
	if rec := a.do(http.MethodGet, "/api/v1/criteria-sets/cs_nosuchset00", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown set: %d", rec.Code)
	}
	rec = a.do(http.MethodPut, "/api/v1/criteria-sets/"+cs.ID, `{"task": "kai:plumbix/X6Q5JY", "owner": "eduard@madmax.local", "criteria": [{"text": "Only this now"}]}`)
	o.check("CriteriaSet", rec)
	if rec.Code != http.StatusOK || len(decode[change.CriteriaSet](t, rec).Criteria) != 1 {
		t.Errorf("update: %d %s", rec.Code, rec.Body.String())
	}
	// A submission names the set and may leave its own criteria out.
	rec = a.do(http.MethodPost, "/api/v1/changes", `{"repo_id": "`+repo.ID+`", "source_branch": "feat", "target_branch": "main", "title": "Add retries", "intent": "Retry flaky uploads", "criteria_set": "`+cs.ID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit with a set: %d %s", rec.Code, rec.Body.String())
	}
	o.check("Change", rec)
	c := decode[change.Change](t, rec)
	if len(c.AcceptanceCriteria) != 1 || c.AcceptanceCriteria[0].Text != "Only this now" || c.CriteriaSet == nil || c.CriteriaSet.ID != cs.ID || c.CriteriaSet.Owner != "eduard@madmax.local" {
		t.Errorf("change = %+v %+v", c.AcceptanceCriteria, c.CriteriaSet)
	}
	used := a.do(http.MethodPut, "/api/v1/criteria-sets/"+cs.ID, `{"task": "kai:plumbix/X6Q5JY", "owner": "eduard@madmax.local", "criteria": [{"text": "too late"}]}`)
	o.check("Error", used)
	if used.Code != http.StatusConflict || decode[apiError](t, used).Error.Code != "criteria_set_used" {
		t.Errorf("edit of a used set: %d %s", used.Code, used.Body.String())
	}
	if got := decode[change.CriteriaSet](t, a.do(http.MethodGet, "/api/v1/criteria-sets/"+cs.ID, "")); len(got.UsedBy) != 1 || got.UsedBy[0] != c.ID {
		t.Errorf("used_by = %v", got.UsedBy)
	}
	if rec := a.do(http.MethodPost, "/api/v1/changes", `{"repo_id": "`+repo.ID+`", "source_branch": "feat2", "target_branch": "main", "title": "t", "intent": "i", "criteria_set": "cs_nosuchset00"}`); rec.Code != http.StatusNotFound {
		t.Errorf("submit naming an unknown set: %d %s", rec.Code, rec.Body.String())
	}
}
