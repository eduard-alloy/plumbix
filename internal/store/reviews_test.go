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
	// Expiry closes a claimed review; a verdict after it is refused.
	if err := db.ExpireReview(ctx, "rv_2", at.Add(time.Hour)); err != nil {
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
	if err := db.ExpireRunReviews(ctx, "run_2", at.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetReview(ctx, "rv_3"); got.Status != review.Expired {
		t.Errorf("review of a recovered run = %+v", got)
	}
	if _, ok, _ := db.ClaimReview(ctx, "", "kai", "hz", at.Add(4*time.Hour)); ok {
		t.Error("a review of a recovered run was handed out")
	}
	// Events: three requested, two claimed, in order, valid against the schema.
	after, _ := db.EventsAfter(ctx, 0, events.Filter{}, 100)
	fresh := after[len(before):]
	var types []events.Type
	for _, e := range fresh {
		types = append(types, e.Type)
		validateEvent(t, schema, e)
	}
	want := []events.Type{events.ReviewRequested, events.ReviewRequested, events.ReviewClaimed, events.ReviewClaimed, events.ReviewRequested}
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
