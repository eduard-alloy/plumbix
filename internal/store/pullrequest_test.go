package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestRepoPullRequestSettingsPersist: pull_requests and merger survive the
// round trip, and repositories stored before WU-054 read as Plumbix-merged.
func TestRepoPullRequestSettingsPersist(t *testing.T) {
	db, _ := openTemp(t)
	r := sampleRepo("r_1", "https://github.com/a/b")
	r.PullRequests, r.Merger = true, change.MergerGitHub
	if err := db.CreateRepo(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRepo(ctx, "r_1")
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("got %+v, %v; want %+v", got, err, r)
	}
	// A repository written without a merger (as before this unit) reads as
	// Plumbix-merged.
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO repos (id, host, url, protected_branches, merge_strategy, created_at) VALUES ('r_old', 'github', 'https://github.com/a/old', '["main"]', 'squash', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	old, err := db.GetRepo(ctx, "r_old")
	if err != nil || old.Merger != change.MergerPlumbix || old.PullRequests {
		t.Fatalf("old repo = %+v, %v", old, err)
	}
}

// TestChangeByPullRequest: a change is found by its repository and PR
// number, and the reference round-trips.
func TestChangeByPullRequest(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	c := sampleChange("c_1", "r_1", "feat")
	c.PullRequest = &change.PullRequestRef{Number: 7, URL: "https://github.com/a/b/pull/7"}
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChange(ctx, "c_1")
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("got %+v, %v; want %+v", got, err, c)
	}
	byPR, err := db.ChangeByPullRequest(ctx, "r_1", 7)
	if err != nil || byPR.ID != "c_1" {
		t.Fatalf("by PR: %+v, %v", byPR, err)
	}
	if _, err := db.ChangeByPullRequest(ctx, "r_1", 8); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown PR: err = %v", err)
	}
	// Only the open change counts: a closed one for the same PR is history.
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.Closed, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ChangeByPullRequest(ctx, "r_1", 7); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("closed change found: err = %v", err)
	}
}

// TestMergeByGitHubHasNoRun: a merge GitHub made may carry no run and
// records who merged (WU-054).
func TestMergeByGitHubHasNoRun(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	rec := change.MergeRecord{ChangeID: "c_1", RepoID: "r_1", Branch: "main", Merge: change.Merge{Commit: "abc", Previous: "000", Strategy: change.StrategyGitHub, MergedBy: "octocat", MergedAt: t0}}
	if err := db.RecordMerge(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChange(ctx, "c_1")
	if err != nil || got.Merge == nil || got.Merge.RunID != "" || got.Merge.MergedBy != "octocat" || got.Merge.Strategy != change.StrategyGitHub {
		t.Fatalf("merge = %+v, %v", got.Merge, err)
	}
	merges, err := db.ListMerges(ctx, "r_1", "main")
	if err != nil || len(merges) != 1 || merges[0].MergedBy != "octocat" || merges[0].RunID != "" {
		t.Fatalf("merges = %+v, %v", merges, err)
	}
}
