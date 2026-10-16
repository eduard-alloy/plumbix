package change_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

func TestPlainGitRemotes(t *testing.T) {
	bare := gittest.New(t).Remote
	working := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", working).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	notGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(notGit, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, url := range []string{
		bare,
		working,
		"file://" + bare,
		"ssh://git@example.com/srv/app.git",
		"ssh://example.com:2222/srv/app.git",
		"git@example.com:team/app.git",
	} {
		if _, err := newService(t).RegisterRepo(ctx, change.RepoInput{Host: change.HostGit, URL: url}); err != nil {
			t.Errorf("%s rejected: %v", url, err)
		}
	}

	for url, want := range map[string]string{
		"relative/path.git":                "absolute",
		filepath.Join(t.TempDir(), "nope"): "not a git repository",
		notGit:                             "not a git repository",
		"file://relative":                  "absolute",
		"https://example.com/app.git":      "github",
		"":                                 "required",
	} {
		_, err := newService(t).RegisterRepo(ctx, change.RepoInput{Host: change.HostGit, URL: url})
		msg := problems(t, err)["url"]
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("%q: problem %q, want it to mention %q", url, msg, want)
		}
	}
}
