package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/run"
)

func TestBranchHeads(t *testing.T) {
	db := withChange(t)
	heads, polled, err := db.KnownHeads(ctx, "r_1")
	if err != nil || polled || len(heads) != 0 {
		t.Fatalf("before first poll: %v %v %v", heads, polled, err)
	}
	if err := db.SaveHeads(ctx, "r_1", map[string]string{"main": "a1", "feat": "b1"}, t0); err != nil {
		t.Fatal(err)
	}
	heads, polled, _ = db.KnownHeads(ctx, "r_1")
	if !polled || !reflect.DeepEqual(heads, map[string]string{"main": "a1", "feat": "b1"}) {
		t.Errorf("after save: %v %v", heads, polled)
	}
	if err := db.SaveHeads(ctx, "r_1", map[string]string{"main": "a2"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if heads, _, _ = db.KnownHeads(ctx, "r_1"); !reflect.DeepEqual(heads, map[string]string{"main": "a2"}) {
		t.Errorf("after replace: %v", heads)
	}
	if err := db.SaveHeads(ctx, "r_1", map[string]string{}, t0); err != nil {
		t.Fatal(err)
	}
	if heads, polled, _ = db.KnownHeads(ctx, "r_1"); !polled || len(heads) != 0 {
		t.Errorf("a repo with no branches is still polled: %v %v", heads, polled)
	}
}

func TestRepoRunsAndBranch(t *testing.T) {
	db := withChange(t)
	for i, r := range []run.Run{
		{ID: "run_a", RepoID: "r_1", Branch: "main", Status: run.Running, StartedAt: t0},
		{ID: "run_b", RepoID: "r_1", Branch: "feat", ChangeID: "c_1", Iteration: 1, Status: run.Running, StartedAt: t0.Add(time.Second)},
		{ID: "run_c", RepoID: "r_1", Branch: "main", Status: run.Running, StartedAt: t0.Add(2 * time.Second)},
	} {
		if err := db.CreateRun(ctx, r); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	all, err := db.ListRepoRuns(ctx, "r_1", "", 10)
	if err != nil || len(all) != 3 || all[0].ID != "run_c" || all[2].ID != "run_a" {
		t.Fatalf("all = %+v, %v", all, err)
	}
	mains, _ := db.ListRepoRuns(ctx, "r_1", "main", 10)
	if len(mains) != 2 || mains[0].Branch != "main" {
		t.Errorf("main runs = %+v", mains)
	}
	if limited, _ := db.ListRepoRuns(ctx, "r_1", "", 1); len(limited) != 1 {
		t.Errorf("limit ignored: %d", len(limited))
	}
	got, _ := db.GetRun(ctx, "run_b")
	if got.Branch != "feat" {
		t.Errorf("branch = %q", got.Branch)
	}
}
