package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

var ctx = context.Background()

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plumbix.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

var t0 = time.Date(2026, 10, 16, 9, 30, 0, 123456789, time.UTC)

func sampleRepo(id, url string) change.Repo {
	return change.Repo{ID: id, Host: "github", URL: url, ProtectedBranches: []string{"main", "release"}, MergeStrategy: "squash", CreatedAt: t0}
}

func sampleChange(id, repoID, branch string) change.Change {
	return change.Change{
		ID: id, RepoID: repoID, SourceBranch: branch, TargetBranch: "main",
		Title: "Add retries", Intent: "Retry flaky uploads",
		AcceptanceCriteria: []change.Criterion{
			{ID: "AC-1", Text: "Uploads retry 3 times", Check: &change.Check{Steps: []string{"test"}}},
			{ID: "AC-2", Text: "Lint is clean", Check: &change.Check{Command: "make lint"}},
			{ID: "AC-3", Text: "Docs mention retries"},
		},
		Constraints: []string{"no new dependencies"}, SubmittedBy: "kai",
		State: change.Open, CreatedAt: t0, UpdatedAt: t0,
	}
}

func TestOpenMigratesAndReopens(t *testing.T) {
	db, path := openTemp(t)
	v, err := db.schemaVersion(ctx)
	if err != nil || v != latestVersion() {
		t.Fatalf("version = %d, %v; want %d", v, err, latestVersion())
	}
	_ = db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if v2, _ := db2.schemaVersion(ctx); v2 != v {
		t.Fatalf("version after reopen = %d, want %d", v2, v)
	}
}

func TestRepoRoundTrip(t *testing.T) {
	db, _ := openTemp(t)
	r := sampleRepo("r_1", "https://github.com/a/b")
	if err := db.CreateRepo(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRepo(ctx, "r_1")
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("got %+v, %v; want %+v", got, err, r)
	}
	if err := db.CreateRepo(ctx, sampleRepo("r_2", "https://github.com/a/b")); !errors.Is(err, change.ErrConflict) {
		t.Errorf("duplicate URL: err = %v, want ErrConflict", err)
	}
	if _, err := db.GetRepo(ctx, "r_missing"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("missing repo: err = %v, want ErrNotFound", err)
	}
	list, err := db.ListRepos(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("list = %v, %v", list, err)
	}
}

func TestChangeRoundTripAndPersistence(t *testing.T) {
	db, path := openTemp(t)
	if err := db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b")); err != nil {
		t.Fatal(err)
	}
	c := sampleChange("c_1", "r_1", "feature/retries")
	if err := db.CreateChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, err := db2.GetChange(ctx, "c_1")
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("after reopen got %+v, %v; want %+v", got, err, c)
	}
	if _, err := db2.GetChange(ctx, "c_missing"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("missing change: err = %v", err)
	}
}

func TestChangeNeedsExistingRepo(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_none", "x")); err == nil {
		t.Fatal("change with unknown repo was stored")
	}
}

func TestOneOpenChangePerBranch(t *testing.T) {
	db, _ := openTemp(t)
	_ = db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b"))
	if err := db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_2", "r_1", "feat")); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("second open change: err = %v, want ErrConflict", err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.Closed, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChange(ctx, sampleChange("c_2", "r_1", "feat")); err != nil {
		t.Fatalf("after closing the first: %v", err)
	}
}

func TestSetChangeState(t *testing.T) {
	db, _ := openTemp(t)
	_ = db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b"))
	_ = db.CreateChange(ctx, sampleChange("c_1", "r_1", "feat"))
	later := t0.Add(time.Hour)
	if err := db.SetChangeState(ctx, "c_1", change.Running, change.Closed, later); !errors.Is(err, change.ErrConflict) {
		t.Errorf("wrong from-state: err = %v, want ErrConflict", err)
	}
	if err := db.SetChangeState(ctx, "c_none", change.Open, change.Closed, later); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown change: err = %v, want ErrNotFound", err)
	}
	if err := db.SetChangeState(ctx, "c_1", change.Open, change.Closed, later); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetChange(ctx, "c_1")
	if got.State != change.Closed || !got.UpdatedAt.Equal(later) || !got.CreatedAt.Equal(t0) {
		t.Errorf("after close: state %s updated %s created %s", got.State, got.UpdatedAt, got.CreatedAt)
	}
}

func TestListChangesFilters(t *testing.T) {
	db, _ := openTemp(t)
	_ = db.CreateRepo(ctx, sampleRepo("r_1", "https://github.com/a/b"))
	_ = db.CreateRepo(ctx, sampleRepo("r_2", "https://github.com/a/c"))
	for i, c := range []change.Change{sampleChange("c_1", "r_1", "x"), sampleChange("c_2", "r_1", "y"), sampleChange("c_3", "r_2", "x")} {
		c.CreatedAt = t0.Add(time.Duration(i) * time.Second)
		if err := db.CreateChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.SetChangeState(ctx, "c_2", change.Open, change.Closed, t0)
	ids := func(f change.Filter) []string {
		list, err := db.ListChanges(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range list {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids(change.Filter{}); !reflect.DeepEqual(got, []string{"c_1", "c_2", "c_3"}) {
		t.Errorf("all = %v", got)
	}
	if got := ids(change.Filter{RepoID: "r_1"}); !reflect.DeepEqual(got, []string{"c_1", "c_2"}) {
		t.Errorf("repo r_1 = %v", got)
	}
	if got := ids(change.Filter{State: change.Open}); !reflect.DeepEqual(got, []string{"c_1", "c_3"}) {
		t.Errorf("open = %v", got)
	}
	if got := ids(change.Filter{RepoID: "r_1", State: change.Closed}); !reflect.DeepEqual(got, []string{"c_2"}) {
		t.Errorf("r_1 closed = %v", got)
	}
}
