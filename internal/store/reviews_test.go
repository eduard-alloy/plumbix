package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/run"
)

func TestReviewsAreClaimedOnceAndAnsweredOnce(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	mine := sampleChange("c_1", "r_1", "feat")
	mine.SubmittedBy = "otto"
	theirs := sampleChange("c_2", "r_1", "fix")
	theirs.SubmittedBy = "kai"
	for _, c := range []change.Change{mine, theirs} {
		if err := db.CreateChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	at := t0
	for _, r := range []run.Run{{ID: "run_1", RepoID: "r_1", ChangeID: "c_1"}, {ID: "run_2", RepoID: "r_1", ChangeID: "c_2"}} {
		r.Iteration, r.Status, r.StartedAt = 1, run.Running, at
		if err := db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	brief := json.RawMessage(`{"prompt":{"system":"s","user":"u"}}`)
	r1 := review.Review{ID: "rv_1", RunID: "run_1", RepoID: "r_1", ChangeID: "c_1", Step: "ai-review", Brief: brief, CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	r2 := review.Review{ID: "rv_2", RunID: "run_2", RepoID: "r_1", ChangeID: "c_2", Step: "ai-review", Brief: brief, CreatedAt: at.Add(time.Second), ExpiresAt: at.Add(time.Hour)}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	for _, r := range []review.Review{r1, r2} {
		if err := db.CreateReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// A verdict needs a claim first: nobody holds a token yet.
	verdict := json.RawMessage(`{"plumbix":"verdict/v1","outcome":"approve","summary":"fine"}`)
	if err := db.PostReviewVerdict(ctx, "rv_1", "", verdict, at); !errors.Is(err, change.ErrConflict) {
		t.Errorf("verdict before a claim: %v", err)
	}
	// otto submitted c_1, so otto gets c_2's review although c_1's is older.
	got, ok, err := db.ClaimReview(ctx, "", "otto", "h2", at.Add(2*time.Second))
	if err != nil || !ok || got.ID != "rv_2" || got.Status != review.Claimed || got.Reviewer != "otto" || string(got.Brief) != string(brief) {
		t.Fatalf("otto's claim = %+v %v %v", got, ok, err)
	}
	// kai gets c_1's; the repo filter applies; nothing is left afterwards.
	if _, ok, _ := db.ClaimReview(ctx, "r_other", "kai", "hx", at); ok {
		t.Error("a claim with another repo's filter took a review")
	}
	got, ok, err = db.ClaimReview(ctx, "r_1", "kai", "h1", at.Add(3*time.Second))
	if err != nil || !ok || got.ID != "rv_1" {
		t.Fatalf("kai's claim = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := db.ClaimReview(ctx, "", "kai", "hy", at); ok {
		t.Error("a third claim found a review")
	}
	// The verdict needs the right token, and lands once.
	if err := db.PostReviewVerdict(ctx, "rv_1", "wrong", verdict, at.Add(time.Minute)); !errors.Is(err, change.ErrConflict) {
		t.Errorf("wrong token: %v", err)
	}
	if err := db.PostReviewVerdict(ctx, "rv_nope", "h1", verdict, at); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown review: %v", err)
	}
	if err := db.PostReviewVerdict(ctx, "rv_1", "h1", verdict, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.PostReviewVerdict(ctx, "rv_1", "h1", verdict, at.Add(2*time.Minute)); !errors.Is(err, change.ErrConflict) {
		t.Errorf("second verdict: %v", err)
	}
	done, err := db.GetReview(ctx, "rv_1")
	if err != nil || done.Status != review.Done || string(done.Verdict) != string(verdict) || !done.FinishedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("done review = %+v %v", done, err)
	}
	// A claim whose reviewer never got the brief is released and taken again.
	if err := db.ReleaseReview(ctx, "rv_2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetReview(ctx, "rv_2"); got.Status != review.Pending || got.Reviewer != "" || got.TokenHash != "" {
		t.Errorf("released review = %+v", got)
	}
	if again, ok, err := db.ClaimReview(ctx, "", "otto", "h2", at.Add(5*time.Second)); err != nil || !ok || again.ID != "rv_2" {
		t.Errorf("claim after release = %+v %v %v", again, ok, err)
	}
	// Expiry closes a claimed review; a verdict after it is refused.
	if err := db.ExpireReview(ctx, "rv_2", at.Add(time.Hour), review.ReasonTimedOut); err != nil {
		t.Fatal(err)
	}
	if err := db.PostReviewVerdict(ctx, "rv_2", "h2", verdict, at.Add(2*time.Hour)); !errors.Is(err, change.ErrConflict) {
		t.Errorf("verdict after expiry: %v", err)
	}
	if exp, _ := db.GetReview(ctx, "rv_2"); exp.Status != review.Expired {
		t.Errorf("expired review = %+v", exp)
	}
	// A restart closes a run's open reviews: nothing is left to claim or answer.
	r3 := review.Review{ID: "rv_3", RunID: "run_2", RepoID: "r_1", ChangeID: "c_2", Step: "ai-review", Brief: brief, CreatedAt: at.Add(3 * time.Hour), ExpiresAt: at.Add(5 * time.Hour)}
	if err := db.CreateReview(ctx, r3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExpireRunReviews(ctx, "run_2", at.Add(4*time.Hour), review.ReasonServerRestart); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetReview(ctx, "rv_3"); got.Status != review.Expired {
		t.Errorf("review of a recovered run = %+v", got)
	}
	if _, ok, _ := db.ClaimReview(ctx, "", "kai", "hz", at.Add(4*time.Hour)); ok {
		t.Error("a review of a recovered run was handed out")
	}
	// Events: requested twice, claimed twice, released, claimed again, expired (rv_2), requested once more, expired (the recovered run's, WU-068); all valid against the schema.
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	var types []events.Type
	for _, e := range fresh {
		types = append(types, e.Type)
		validateEvent(t, schema, e)
	}
	want := []events.Type{events.ReviewRequested, events.ReviewRequested, events.ReviewClaimed, events.ReviewClaimed, events.ReviewReleased, events.ReviewClaimed, events.ReviewExpired, events.ReviewRequested, events.ReviewExpired}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
	raw, _ := json.Marshal(fresh[2].Payload)
	var p events.ReviewClaimedPayload
	if json.Unmarshal(raw, &p) != nil || p.ReviewID != "rv_2" || p.Reviewer != "otto" {
		t.Errorf("claimed payload = %s", raw)
	}
	if fresh[0].RunID != "run_1" || fresh[0].ChangeID != "c_1" {
		t.Errorf("requested event keys = %+v", fresh[0])
	}
}

// TestExpiredReviewSaysWhy: a review the platform closes says why, on the
// row and in review.expired; a run's expiry names each open review with
// the reason given and leaves closed ones alone; a release says the
// reviewer went away (WU-068).
func TestExpiredReviewSaysWhy(t *testing.T) {
	db := openDB(t)
	schema := compileEventsSchema(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	c := sampleChange("c_1", "r_1", "feat")
	c.SubmittedBy = "someone" // neither reviewer submitted it, so both may claim
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	at := t0
	if err := db.CreateRun(ctx, run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Iteration: 1, Status: run.Running, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	brief := json.RawMessage(`{}`)
	for i, id := range []string{"rv_1", "rv_2", "rv_3"} {
		if err := db.CreateReview(ctx, review.Review{ID: id, RunID: "run_1", RepoID: "r_1", ChangeID: "c_1", Step: "ai-review", Brief: brief, CreatedAt: at.Add(time.Duration(i) * time.Second), ExpiresAt: at.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	// kai holds rv_1; otto takes rv_2 and goes away (released below); rv_3 is never claimed.
	if got, ok, err := db.ClaimReview(ctx, "", "kai", "h1", at.Add(5*time.Second)); err != nil || !ok || got.ID != "rv_1" {
		t.Fatalf("kai's claim = %+v %v %v", got, ok, err)
	}
	if got, ok, err := db.ClaimReview(ctx, "", "otto", "h3", at.Add(6*time.Second)); err != nil || !ok || got.ID != "rv_2" {
		t.Fatalf("otto's claim = %+v %v %v", got, ok, err)
	}
	before, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	if err := db.ExpireReview(ctx, "rv_1", at.Add(time.Minute), review.ReasonTimedOut); err != nil {
		t.Fatal(err)
	}
	if rv, err := db.GetReview(ctx, "rv_1"); err != nil || rv.Status != review.Expired || rv.ExpiredReason != review.ReasonTimedOut || !rv.FinishedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("rv_1 = %+v, %v", rv, err)
	}
	// Expiring again changes nothing and says nothing; an unknown id is an error.
	if err := db.ExpireReview(ctx, "rv_1", at.Add(2*time.Minute), review.ReasonServerStopped); err != nil {
		t.Errorf("second expiry: %v", err)
	}
	if err := db.ExpireReview(ctx, "rv_nope", at, review.ReasonTimedOut); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown review: %v", err)
	}
	if err := db.ReleaseReview(ctx, "rv_2"); err != nil {
		t.Fatal(err)
	}
	// The server restarts: every open review of the run expires with that
	// reason, and the closed ones come back for the step logs.
	closed, err := db.ExpireRunReviews(ctx, "run_1", at.Add(3*time.Minute), review.ReasonServerRestart)
	if err != nil || len(closed) != 2 || closed[0].ID != "rv_2" || closed[1].ID != "rv_3" || closed[0].ExpiredReason != review.ReasonServerRestart {
		t.Fatalf("closed = %+v, %v", closed, err)
	}
	for _, id := range []string{"rv_2", "rv_3"} {
		rv, err := db.GetReview(ctx, id)
		if err != nil || rv.Status != review.Expired || rv.ExpiredReason != review.ReasonServerRestart {
			t.Errorf("%s = %+v, %v", id, rv, err)
		}
	}
	if rv, _ := db.GetReview(ctx, "rv_1"); rv.ExpiredReason != review.ReasonTimedOut {
		t.Errorf("rv_1 after the run's expiry = %+v", rv)
	}
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	got := after[len(before):]
	want := []struct {
		ty      events.Type
		id, who string
		reason  string
	}{
		{events.ReviewExpired, "rv_1", "kai", review.ReasonTimedOut},
		{events.ReviewReleased, "rv_2", "otto", review.ReasonReviewerGone},
		{events.ReviewExpired, "rv_2", "", review.ReasonServerRestart},
		{events.ReviewExpired, "rv_3", "", review.ReasonServerRestart},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v", got)
	}
	for i, w := range want {
		e := got[i]
		validateEvent(t, schema, e)
		raw, _ := json.Marshal(e.Payload)
		var p struct {
			ReviewID string `json:"review_id"`
			Reviewer string `json:"reviewer"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &p)
		if e.Type != w.ty || p.ReviewID != w.id || p.Reviewer != w.who || p.Reason != w.reason || e.RunID != "run_1" || e.ChangeID != "c_1" {
			t.Errorf("event %d = %s %s: %+v, want %+v", i, e.Type, raw, e, w)
		}
	}
}
