package diff

import (
	"strings"
	"testing"
)

const sample = `diff --git a/internal/evidence/upload.go b/internal/evidence/upload.go
index 1111111..2222222 100644
--- a/internal/evidence/upload.go
+++ b/internal/evidence/upload.go
@@ -68,6 +68,9 @@ func (u *Uploader) put(ctx context.Context, key string, body []byte) error {
 	for attempt := 1; ; attempt++ {
 		err := u.try(ctx, key, body)
 		if err == nil {
+			if attempt > 1 {
+				fmt.Fprintf(u.log, "upload %s succeeded on attempt %d\n", key, attempt)
+			}
 			return nil
 		}
 		if attempt == 3 {
@@ -80,4 +83,3 @@ func (u *Uploader) put(ctx context.Context, key string, body []byte) error {
 		time.Sleep(backoff(attempt))
 	}
 }
-// end
diff --git a/docs/notes.txt b/docs/notes.txt
deleted file mode 100644
index 3333333..0000000
--- a/docs/notes.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-first
-second
\ No newline at end of file
diff --git a/new dir/new file.md b/new dir/new file.md
new file mode 100644
index 0000000..4444444
--- /dev/null
+++ b/new dir/new file.md
@@ -0,0 +1 @@
+hello
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..5555555
Binary files /dev/null and b/logo.png differ
diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`

func TestParse(t *testing.T) {
	files, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("parsed %d files, want 5: %+v", len(files), files)
	}

	up := files[0]
	if up.Path != "internal/evidence/upload.go" || up.Status != Modified || up.Additions != 3 || up.Deletions != 1 || len(up.Hunks) != 2 {
		t.Fatalf("upload.go = %+v", up)
	}
	h := up.Hunks[0]
	if h.OldStart != 68 || h.OldLines != 6 || h.NewStart != 68 || h.NewLines != 9 || !strings.HasPrefix(h.Header, "func (u *Uploader) put") {
		t.Errorf("hunk 1 = %+v", h)
	}
	want := []Line{
		{Type: Context, Old: 68, New: 68, Text: "\tfor attempt := 1; ; attempt++ {"},
		{Type: Context, Old: 69, New: 69, Text: "\t\terr := u.try(ctx, key, body)"},
		{Type: Context, Old: 70, New: 70, Text: "\t\tif err == nil {"},
		{Type: Add, New: 71, Text: "\t\t\tif attempt > 1 {"},
		{Type: Add, New: 72, Text: "\t\t\t\tfmt.Fprintf(u.log, \"upload %s succeeded on attempt %d\\n\", key, attempt)"},
		{Type: Add, New: 73, Text: "\t\t\t}"},
		{Type: Context, Old: 71, New: 74, Text: "\t\t\treturn nil"},
		{Type: Context, Old: 72, New: 75, Text: "\t\t}"},
		{Type: Context, Old: 73, New: 76, Text: "\t\tif attempt == 3 {"},
	}
	if len(h.Lines) != len(want) {
		t.Fatalf("hunk 1 has %d lines, want %d: %+v", len(h.Lines), len(want), h.Lines)
	}
	for i := range want {
		if h.Lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, h.Lines[i], want[i])
		}
	}
	if last := up.Hunks[1].Lines[3]; last.Type != Del || last.Old != 83 || last.New != 0 || last.Text != "// end" {
		t.Errorf("deleted line = %+v", last)
	}

	del := files[1]
	if del.Path != "docs/notes.txt" || del.Status != Deleted || del.Additions != 0 || del.Deletions != 2 || len(del.Hunks) != 1 || len(del.Hunks[0].Lines) != 2 {
		t.Errorf("deleted file = %+v; the no-newline marker is not a line", del)
	}
	added := files[2]
	if added.Path != "new dir/new file.md" || added.Status != Added || added.Additions != 1 || added.Hunks[0].NewLines != 1 || added.Hunks[0].OldLines != 0 {
		t.Errorf("added file with spaces = %+v", added)
	}
	bin := files[3]
	if bin.Path != "logo.png" || !bin.Binary || bin.Status != Added || len(bin.Hunks) != 0 {
		t.Errorf("binary file = %+v", bin)
	}
	mode := files[4]
	if mode.Path != "script.sh" || mode.Status != Modified || len(mode.Hunks) != 0 || mode.Additions != 0 {
		t.Errorf("mode-only change = %+v", mode)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if files, err := Parse(nil); err != nil || len(files) != 0 {
		t.Errorf("empty diff = %v, %v", files, err)
	}
	for _, bad := range []string{
		"@@ -1,2 +1,2 @@\n a\n",                                       // hunk before any file
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ nonsense @@\n",      // malformed hunk header
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n?weird\n", // unknown line prefix
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTotals(t *testing.T) {
	files, _ := Parse([]byte(sample))
	d := Diff{Base: "a", Head: "b", Files: files}
	d.Total()
	if d.Additions != 4 || d.Deletions != 3 {
		t.Errorf("totals = +%d -%d, want +4 -3", d.Additions, d.Deletions)
	}
	s := d.Stat()
	if s.Additions != 4 || len(s.Files) != 5 || s.Files[0].Hunks != nil || len(d.Files[0].Hunks) != 2 {
		t.Errorf("Stat must drop hunks on a copy: %+v", s.Files[0])
	}
}
