package store

import (
	"errors"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

func TestMergeRecordShowsOnChange(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	c := sampleChange("c_1", "r_1", "feat")
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, run.Run{ID: "run_1", RepoID: "r_1", ChangeID: "c_1", Branch: "feat", Iteration: 1, Status: run.Running, StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChange(ctx, "c_1")
	if err != nil || got.Merge != nil {
		t.Fatalf("before merge: %+v, %v", got.Merge, err)
	}
	rec := change.MergeRecord{ChangeID: "c_1", RepoID: "r_1", Branch: "main", Merge: change.Merge{Commit: "abc", Previous: "000", Strategy: "squash", RunID: "run_1", MergedAt: t0}}
	if err := db.RecordMerge(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMerge(ctx, rec); !errors.Is(err, change.ErrConflict) {
		t.Errorf("second record err = %v", err)
	}
	got, err = db.GetChange(ctx, "c_1")
	if err != nil || got.Merge == nil || got.Merge.Commit != "abc" || got.Merge.RunID != "run_1" || !got.Merge.MergedAt.Equal(t0) {
		t.Fatalf("after merge: %+v, %v", got.Merge, err)
	}
	list, err := db.ListChanges(ctx, change.Filter{RepoID: "r_1"})
	if err != nil || len(list) != 1 || list[0].Merge == nil {
		t.Fatalf("list: %+v, %v", list, err)
	}
	merges, err := db.ListMerges(ctx, "r_1", "main")
	if err != nil || len(merges) != 1 || merges[0].ChangeID != "c_1" || merges[0].Previous != "000" {
		t.Fatalf("merges: %+v, %v", merges, err)
	}
	if other, _ := db.ListMerges(ctx, "r_1", "release"); len(other) != 0 {
		t.Errorf("other branch has merges: %+v", other)
	}
}

func TestAlerts(t *testing.T) {
	db, _ := openTemp(t)
	a, created, err := db.Raise(ctx, alert.Alert{Kind: alert.BypassCommit, Key: "main@abc", RepoID: "r_1", Message: "foreign commit", Details: map[string]any{"sha": "abc"}, CreatedAt: t0})
	if err != nil || !created || a.ID == "" {
		t.Fatalf("raise: %+v, %v, %v", a, created, err)
	}
	again, created, err := db.Raise(ctx, alert.Alert{Kind: alert.BypassCommit, Key: "main@abc", Message: "dup", CreatedAt: t0.Add(time.Minute)})
	if err != nil || created || again.ID != a.ID || again.Message != "foreign commit" {
		t.Fatalf("raise again: %+v, %v, %v", again, created, err)
	}
	if _, _, err := db.Raise(ctx, alert.Alert{Kind: alert.TokenExpiry, Key: "github", Message: "expires soon", CreatedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	open, err := db.List(ctx, false)
	if err != nil || len(open) != 2 || open[0].Kind != alert.TokenExpiry || open[1].Details["sha"] != "abc" {
		t.Fatalf("open: %+v, %v", open, err)
	}
	if err := db.Resolve(ctx, a.ID, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Get(ctx, a.ID); err != nil || got.ResolvedAt == nil || got.Details["sha"] != "abc" {
		t.Errorf("get: %+v, %v", got, err)
	}
	if _, err := db.Get(ctx, "al_missing"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
	if err := db.Resolve(ctx, a.ID, t0.Add(3*time.Hour)); err != nil {
		t.Errorf("resolving twice: %v", err)
	}
	if err := db.Resolve(ctx, "al_missing", t0); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := db.ResolveKey(ctx, alert.TokenExpiry, "github", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if open, _ = db.List(ctx, false); len(open) != 0 {
		t.Errorf("still open: %+v", open)
	}
	all, err := db.List(ctx, true)
	if err != nil || len(all) != 2 || all[1].ResolvedAt == nil || !all[1].ResolvedAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("all: %+v, %v", all, err)
	}
	// A resolved subject can be raised again.
	if _, created, err := db.Raise(ctx, alert.Alert{Kind: alert.BypassCommit, Key: "main@abc", Message: "back", CreatedAt: t0}); err != nil || !created {
		t.Errorf("re-raise: %v, %v", created, err)
	}
}
