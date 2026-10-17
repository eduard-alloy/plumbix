package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/run"
)

const externalPipeline = "version: 1\nsteps:\n  - {name: test, image: x, run: x}\n  - {name: validate, image: v, validator: true, needs: [test]}\n  - {name: ai-review, validator: true, external: true, needs: [test], timeout: 30s}\n"

func TestExternalReviewsAreClaimedAndAnsweredOverTheAPI(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalPipeline}, "external review")
	// The operator allows full tools; the brief carries it.
	if rec := a.do(http.MethodPut, "/api/v1/settings", `{"ai_tools": "full"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ai_tools":"full"`) {
		t.Fatalf("set tools: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodPut, "/api/v1/settings", `{"ai_tools": "everything"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad tools: %d %s", rec.Code, rec.Body.String())
	}
	// A branch cut after the pipeline change, so its run has the external step.
	a.repo.Commit("ext", map[string]string{"b.go": "package b\n"}, "feature on the new pipeline")
	c := decode[change.Change](t, a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "ext")))

	// A reviewer must say who it is.
	anon := a.do(http.MethodGet, "/api/v1/reviews/next?wait=0", "")
	o.check("Error", anon)
	if anon.Code != http.StatusBadRequest || !strings.Contains(anon.Body.String(), `"reviewer"`) {
		t.Errorf("claim without a name: %d %s", anon.Code, anon.Body.String())
	}
	// Long-polling returns the brief once the run reaches the external step.
	rec := a.do(http.MethodGet, "/api/v1/reviews/next?reviewer=kai&wait=20", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
	}
	o.check("ReviewBrief", rec)
	b := decode[review.Brief](t, rec)
	if b.ChangeID != c.ID || b.Step != "ai-review" || b.Token == "" || b.Tools != review.ToolsFull || b.Prompt.System == "" || len(b.Criteria) != 1 || b.Schema["type"] != "object" {
		t.Errorf("brief = %+v", b)
	}
	// Nothing else is waiting, so a short poll answers 204.
	if rec := a.do(http.MethodGet, "/api/v1/reviews/next?reviewer=otto&wait=0", ""); rec.Code != http.StatusNoContent {
		t.Errorf("idle claim: %d %s", rec.Code, rec.Body.String())
	}
	verdict := `{"plumbix":"verdict/v1","outcome":"approve","summary":"Does what it says.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`
	bad := a.do(http.MethodPost, "/api/v1/reviews/"+b.ID+"/verdict", `{"token": "`+b.Token+`", "verdict": {"plumbix":"verdict/v1","outcome":"maybe","summary":"?"}}`)
	o.check("Error", bad)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "outcome") {
		t.Errorf("invalid verdict: %d %s", bad.Code, bad.Body.String())
	}
	wrong := a.do(http.MethodPost, "/api/v1/reviews/"+b.ID+"/verdict", `{"token": "nope", "verdict": `+verdict+`}`)
	o.check("Error", wrong)
	if wrong.Code != http.StatusConflict || decode[apiError](t, wrong).Error.Code != "conflict" {
		t.Errorf("wrong token: %d %s", wrong.Code, wrong.Body.String())
	}
	if rec := a.do(http.MethodPost, "/api/v1/reviews/rv_nope/verdict", `{"token": "x", "verdict": `+verdict+`}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown review: %d %s", rec.Code, rec.Body.String())
	}
	ok := a.do(http.MethodPost, "/api/v1/reviews/"+b.ID+"/verdict", `{"token": "`+b.Token+`", "verdict": `+verdict+`}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("post verdict: %d %s", ok.Code, ok.Body.String())
	}
	o.check("ReviewAccepted", ok)
	if again := a.do(http.MethodPost, "/api/v1/reviews/"+b.ID+"/verdict", `{"token": "`+b.Token+`", "verdict": `+verdict+`}`); again.Code != http.StatusConflict {
		t.Errorf("second verdict: %d %s", again.Code, again.Body.String())
	}
	if r := a.waitRun(b.RunID); r.Outcome != change.Approved {
		t.Errorf("run = %s %+v", r.Outcome, r.Result)
	}
	// The step log tells the story.
	if rec := a.do(http.MethodGet, "/api/v1/runs/"+b.RunID+"/steps/ai-review/log", ""); !strings.Contains(rec.Body.String(), "claimed by kai") || !strings.Contains(rec.Body.String(), "verdict posted by kai") {
		t.Errorf("step log: %s", rec.Body.String())
	}
	var steps []run.Step
	for _, s := range decode[run.Run](t, a.do(http.MethodGet, "/api/v1/runs/"+b.RunID, "")).Steps {
		if s.Name == "ai-review" {
			steps = append(steps, s)
		}
	}
	if len(steps) != 1 || steps[0].Status != "succeeded" {
		t.Errorf("external step = %+v", steps)
	}
}

func TestAClaimForAPollerThatHungUpIsReleased(t *testing.T) {
	a := newRunAPI(t)
	a.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": externalPipeline}, "external review")
	a.repo.Commit("ext", map[string]string{"b.go": "package b\n"}, "feature on the new pipeline")
	a.do(http.MethodPost, "/api/v1/changes", changeBody(a.repoID, "ext"))
	// The poller's connection drops between the claim and the write.
	old := clientGone
	clientGone = func(*http.Request) bool { return true }
	rec := a.do(http.MethodGet, "/api/v1/reviews/next?reviewer=kai&wait=20", "")
	clientGone = old
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("abandoned claim answered %d %q; want nothing written", rec.Code, rec.Body.String())
	}
	// The review is pending again: the next reviewer gets it with a fresh token.
	again := a.do(http.MethodGet, "/api/v1/reviews/next?reviewer=otto&wait=5", "")
	if again.Code != http.StatusOK {
		t.Fatalf("claim after a release: %d %s", again.Code, again.Body.String())
	}
	b := decode[review.Brief](t, again)
	verdict := `{"plumbix":"verdict/v1","outcome":"approve","summary":"Fine.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}]}`
	if ok := a.do(http.MethodPost, "/api/v1/reviews/"+b.ID+"/verdict", `{"token": "`+b.Token+`", "verdict": `+verdict+`}`); ok.Code != http.StatusOK {
		t.Errorf("post after re-claim: %d %s", ok.Code, ok.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/runs/"+b.RunID+"/steps/ai-review/log", ""); !strings.Contains(rec.Body.String(), "released") {
		t.Logf("step log (release may land after the verdict): %s", rec.Body.String())
	}
}
