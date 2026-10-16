package gitops

import (
	"reflect"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/diff"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

// TestDiffStat: statuses and counts for added, modified, deleted and binary
// files, with no rename detection.
func TestDiffStat(t *testing.T) {
	repo := gittest.New(t)
	base := repo.Commit("main", map[string]string{"keep.txt": "a\nb\n", "gone.txt": "x\n", "old-name.txt": "same content\n"}, "base")
	repo.Commit("feat", map[string]string{"keep.txt": "a\nb\nc\n", "new.txt": "n\n", "pic.png": "\x89PNG\r\n\x1a\n\x00\x00binary"}, "change")
	repo.Commit("feat", map[string]string{"gone.txt": "", "old-name.txt": "", "new-name.txt": "same content\n"}, "delete and rename by hand")
	head := repo.Commit("feat", map[string]string{"new.txt": "n\nm\n"}, "more")
	m := mirrorOf(t, repo.Remote)

	files, err := m.DiffStat(ctx, base, head)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]diff.File{
		"keep.txt":     {Path: "keep.txt", Status: diff.Modified, Additions: 1},
		"new.txt":      {Path: "new.txt", Status: diff.Added, Additions: 2},
		"gone.txt":     {Path: "gone.txt", Status: diff.Deleted, Deletions: 1},
		"old-name.txt": {Path: "old-name.txt", Status: diff.Deleted, Deletions: 1},
		"new-name.txt": {Path: "new-name.txt", Status: diff.Added, Additions: 1},
		"pic.png":      {Path: "pic.png", Status: diff.Added, Binary: true},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files %+v, want %d", len(files), files, len(want))
	}
	for i, f := range files {
		w, ok := want[f.Path]
		if !ok || !reflect.DeepEqual(f, w) {
			t.Errorf("file %d = %+v, want %+v", i, f, w)
		}
		if i > 0 && files[i-1].Path > f.Path {
			t.Errorf("files are not sorted by path: %s after %s", f.Path, files[i-1].Path)
		}
	}

	// The unified diff agrees with the stat and parses.
	unified, err := m.Diff(ctx, base, head)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := diff.Parse(unified)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(files) {
		t.Fatalf("parsed %d files, stat %d", len(parsed), len(files))
	}
	for i := range parsed {
		p, s := parsed[i], files[i]
		if p.Path != s.Path || p.Status != s.Status || p.Binary != s.Binary || p.Additions != s.Additions || p.Deletions != s.Deletions {
			t.Errorf("parsed %+v disagrees with stat %+v", p, s)
		}
	}
}
