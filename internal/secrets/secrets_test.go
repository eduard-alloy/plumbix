package secrets

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSyntax(t *testing.T) {
	src := `# comment
ANTHROPIC_API_KEY=sk-test-123
export GITHUB_TOKEN=ghp_abc
QUOTED="has spaces = and #"
SINGLE='single quoted'
EMPTY=
  INDENTED = trimmed

# PLUMBIX_AI_MODEL=claude-opus-5-5
`
	got, err := Load(write(t, src, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_API_KEY": "sk-test-123",
		"GITHUB_TOKEN":      "ghp_abc",
		"QUOTED":            "has spaces = and #",
		"SINGLE":            "single quoted",
		"INDENTED":          "trimmed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestLoadRefusesLoosePermissions(t *testing.T) {
	_, err := Load(write(t, "A=b\n", 0o644))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsBadLines(t *testing.T) {
	for _, src := range []string{"NOEQUALS\n", "1BAD=x\n", "A=1\nA=2\n", `A="unterminated` + "\n"} {
		_, err := Load(write(t, src, 0o600))
		if err == nil {
			t.Errorf("%q accepted", src)
			continue
		}
		if strings.Contains(err.Error(), "=1") || strings.Contains(err.Error(), "=2") {
			t.Errorf("error leaks a value: %v", err)
		}
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	_, err := Load(write(t, "GOOD=topsecret\nbad line with topsecret\n", 0o600))
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("err = %v; must not echo file content", err)
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected error")
	}
}
