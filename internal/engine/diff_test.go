package engine

import (
	"errors"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/diff"
)

func fileByPath(files []diff.File, path string) (diff.File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return diff.File{}, false
}

// TestDiffOfLatestRun: the diff is base..head of the newest run that has a
// head, with hunks on request and stats always.
func TestDiffOfLatestRun(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n\nfunc A() {}\n", "notes.txt": "one\ntwo\n"}, "feature")
	c := h.submit("feat")
	if _, err := h.eng.Diff(ctx, c.ID, true); !errors.Is(err, change.ErrInvalidState) {
		t.Fatalf("diff before any run: %v, want ErrInvalidState", err)
	}
	if _, err := h.eng.Diff(ctx, "c_nope", true); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("diff of unknown change: %v", err)
	}
	r := h.wait(h.validate(c.ID).ID)

	d, err := h.eng.Diff(ctx, c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != r.Base || d.Head != r.Head || d.Truncated || len(d.Files) != 2 || d.Additions != 5 || d.Deletions != 0 {
		t.Fatalf("diff = %+v (run base %s head %s)", d, r.Base, r.Head)
	}
	a, ok := fileByPath(d.Files, "a.go")
	if !ok || a.Status != diff.Added || a.Additions != 3 || len(a.Hunks) != 1 || len(a.Hunks[0].Lines) != 3 || a.Hunks[0].Lines[0].Text != "package a" {
		t.Errorf("a.go = %+v", a)
	}
	stat, err := h.eng.Diff(ctx, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Additions != 5 || len(stat.Files) != 2 || stat.Files[0].Hunks != nil {
		t.Errorf("stat = %+v; want totals without hunks", stat)
	}

	// A second iteration moves the head: modified and deleted files show up.
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n\nfunc A() int { return 1 }\n", "notes.txt": ""}, "change a, drop notes")
	r2 := h.wait(h.validate(c.ID).ID)
	d, err = h.eng.Diff(ctx, c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Head != r2.Head || r2.Head == r.Head {
		t.Fatalf("diff head %s, run 2 head %s, run 1 head %s", d.Head, r2.Head, r.Head)
	}
	if a, _ := fileByPath(d.Files, "a.go"); a.Status != diff.Added || a.Additions != 3 {
		t.Errorf("a.go after iteration 2 = %+v; still added relative to main", a)
	}
	if _, ok := fileByPath(d.Files, "notes.txt"); ok {
		t.Errorf("notes.txt was added then deleted on the branch; it is not part of the diff against main: %+v", d.Files)
	}
}
