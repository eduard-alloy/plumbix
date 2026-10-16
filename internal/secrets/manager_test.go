package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerSetsReplacesAndRemovesKeepingTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# git hosts\nPLUMBIX_GITHUB_TOKEN=\nexport OTHER='keep me'\n\n# PLUMBIX_AI_MODEL=claude-x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Values(); len(v) != 1 || v["OTHER"] != "keep me" {
		t.Fatalf("initial values = %v", v)
	}
	notified := 0
	m.Watch(func() { notified++ })

	if err := m.Set("PLUMBIX_GITHUB_TOKEN", "github_pat_abc"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("ANTHROPIC_API_KEY", "sk-ant with space #hash"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("OTHER", "replaced"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PLUMBIX_GITHUB_TOKEN": "github_pat_abc", "ANTHROPIC_API_KEY": "sk-ant with space #hash", "OTHER": "replaced"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if v, ok := m.Get("ANTHROPIC_API_KEY"); !ok || v != want["ANTHROPIC_API_KEY"] {
		t.Errorf("Get = %q, %v", v, ok)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	if !strings.HasPrefix(text, "# git hosts\n") || !strings.Contains(text, "# PLUMBIX_AI_MODEL=claude-x") {
		t.Errorf("comments were not kept:\n%s", text)
	}
	if strings.Count(text, "PLUMBIX_GITHUB_TOKEN=") != 1 || strings.Count(text, "OTHER=") != 1 {
		t.Errorf("keys duplicated:\n%s", text)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	if err := m.Unset("OTHER"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("OTHER"); ok {
		t.Error("OTHER still set after Unset")
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "OTHER") {
		t.Errorf("OTHER line still in the file:\n%s", raw)
	}
	if err := m.Unset("NEVER_SET"); err != nil {
		t.Errorf("unsetting a missing key: %v", err)
	}
	if notified != 4 {
		t.Errorf("watchers called %d times, want 4 (three sets and one effective unset)", notified)
	}
	keys := m.Keys()
	if len(keys) != 2 || keys[0] != "ANTHROPIC_API_KEY" || keys[1] != "PLUMBIX_GITHUB_TOKEN" {
		t.Errorf("Keys = %v, want sorted set keys", keys)
	}
}

func TestManagerCreatesTheFileAndRefusesBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", ".env")
	m, err := Open(path)
	if err != nil {
		t.Fatalf("a missing file is an empty store: %v", err)
	}
	if len(m.Values()) != 0 {
		t.Fatal("values from a missing file")
	}
	for _, bad := range [][2]string{{"1BAD", "x"}, {"has-dash", "x"}, {"", "x"}, {"OK", "line\nbreak"}, {"OK", "both 'quote' kinds \""}} {
		if err := m.Set(bad[0], bad[1]); err == nil {
			t.Errorf("Set(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if err := m.Set("OK", ""); err == nil {
		t.Error("an empty value is Unset, not Set")
	}
	if err := m.Set("TOKEN", "v"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("created file: %v, mode %v", err, info.Mode())
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("created dir mode = %v, want 0700", dir.Mode().Perm())
	}
	if got, _ := Load(path); got["TOKEN"] != "v" {
		t.Errorf("Load = %v", got)
	}
}

func TestManagerRefusesLoosePermissionsOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("Open with 0644: %v", err)
	}
}

// TestManagerSeesHandEdits: a file edited while the server runs is read
// again on the next use, so hand edits and browser edits coexist.
func TestManagerSeesHandEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := m.Values(); v["A"] != "1" {
		t.Fatalf("values = %v", v)
	}
	time.Sleep(20 * time.Millisecond) // a distinct mtime on coarse filesystems
	if err := os.WriteFile(path, []byte("A=2\nB=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if v := m.Values(); v["A"] != "2" || v["B"] != "3" {
		t.Fatalf("values after a hand edit = %v", v)
	}
	if err := m.Set("C", "4"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(path); got["A"] != "2" || got["B"] != "3" || got["C"] != "4" {
		t.Fatalf("file after Set keeps the hand edit: %v", got)
	}
}
