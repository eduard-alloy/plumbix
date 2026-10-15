package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestEnsureTokenCreatesDirAndFile(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "plumbix")
	tok, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hex64.MatchString(tok) {
		t.Errorf("token %q is not 64 hex characters", tok)
	}
	dirInfo, err := os.Stat(dataDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %v, err %v; want 0700", dirInfo.Mode().Perm(), err)
	}
	fileInfo, err := os.Stat(filepath.Join(dataDir, TokenFile))
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, err %v; want 0600", fileInfo.Mode().Perm(), err)
	}
}

func TestEnsureTokenReusesExisting(t *testing.T) {
	dataDir := t.TempDir()
	first, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("second call generated a new token")
	}
}

func TestTokensAreRandom(t *testing.T) {
	a, _ := EnsureToken(t.TempDir())
	b, _ := EnsureToken(t.TempDir())
	if a == b {
		t.Error("two data directories got the same token")
	}
}

func TestLoosePermissionsRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		dataDir := t.TempDir()
		if _, err := EnsureToken(dataDir); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dataDir, TokenFile)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := EnsureToken(dataDir)
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %v: err = %v, want refusal explaining chmod 600", mode, err)
		}
		if _, err := ReadToken(dataDir); err == nil {
			t.Errorf("mode %v: ReadToken accepted loose permissions", mode)
		}
	}
}

func TestMalformedTokenRefused(t *testing.T) {
	for _, content := range []string{"", "short", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, TokenFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := EnsureToken(dataDir)
		if err == nil || !strings.Contains(err.Error(), "delete") {
			t.Errorf("content %q: err = %v, want refusal suggesting deletion", content, err)
		}
	}
}

func TestTrailingNewlineAccepted(t *testing.T) {
	dataDir := t.TempDir()
	tok := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(dataDir, TokenFile), []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadToken(dataDir)
	if err != nil || got != tok {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadTokenMissing(t *testing.T) {
	_, err := ReadToken(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "plumbix serve") {
		t.Fatalf("err = %v, want hint to run plumbix serve", err)
	}
}
