package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

const secretToken = "ghp_SECRET_test_token_123"

func askpass(t *testing.T) string {
	t.Helper()
	p, err := EnsureAskpass(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAskpassScriptHoldsNoSecret(t *testing.T) {
	p := askpass(t)
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("askpass mode = %v, %v", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), secretToken) || !strings.Contains(string(data), "PLUMBIX_GIT_TOKEN") {
		t.Errorf("script = %s", data)
	}
}

func TestFetchWithAuth(t *testing.T) {
	r := gittest.New(t)
	head := r.Commit("feat", map[string]string{"a.txt": "a\n"}, "a")
	url := r.ServeHTTP("x-access-token", secretToken)
	ap := askpass(t)

	m := Open(filepath.Join(t.TempDir(), "m.git"), url).WithAuth(Auth{User: "x-access-token", Token: secretToken}, ap)
	if err := m.Fetch(ctx); err != nil {
		t.Fatalf("fetch with token: %v", err)
	}
	if got, _ := m.Head(ctx, "feat"); got != head {
		t.Errorf("head = %s, want %s", got, head)
	}

	for name, mirror := range map[string]*Mirror{
		"no auth":     Open(filepath.Join(t.TempDir(), "m.git"), url),
		"wrong token": Open(filepath.Join(t.TempDir(), "m.git"), url).WithAuth(Auth{User: "x-access-token", Token: "wrong"}, ap),
	} {
		err := mirror.Fetch(ctx)
		if err == nil {
			t.Errorf("%s: fetch succeeded", name)
			continue
		}
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("%s: error leaks the token", name)
		}
	}
}

func TestListHeads(t *testing.T) {
	r := gittest.New(t)
	feat := r.Commit("feat", map[string]string{"a.txt": "a\n"}, "a")
	main := r.Commit("main", map[string]string{"b.txt": "b\n"}, "b")
	url := r.ServeHTTP("x-access-token", secretToken)
	m := Open(filepath.Join(t.TempDir(), "never-created.git"), url).WithAuth(Auth{User: "x-access-token", Token: secretToken}, askpass(t))
	heads, err := m.ListHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if heads["feat"] != feat || heads["main"] != main || len(heads) != 2 {
		t.Errorf("heads = %v", heads)
	}
}

func TestTokenNeverInArguments(t *testing.T) {
	m := Open("/tmp/m.git", "https://github.com/a/b").WithAuth(Auth{User: "x-access-token", Token: secretToken}, "/tmp/askpass.sh")
	cmd := m.command(ctx, "fetch", "origin")
	if strings.Contains(strings.Join(cmd.Args, " "), secretToken) {
		t.Fatalf("token in arguments: %v", cmd.Args)
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"PLUMBIX_GIT_TOKEN=" + secretToken, "GIT_ASKPASS=/tmp/askpass.sh", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "credential.helper=") {
		t.Error("personal credential helpers are not disabled")
	}
}

func TestSSHRunsInBatchMode(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	cmd := Open("/tmp/m.git", "git@example.com:a/b.git").command(ctx, "fetch")
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "GIT_SSH_COMMAND=ssh -o BatchMode=yes") {
		t.Error("SSH can prompt for a password and hang")
	}
	t.Setenv("GIT_SSH_COMMAND", "my-ssh -i key")
	cmd = Open("/tmp/m.git", "git@example.com:a/b.git").command(ctx, "fetch")
	env := strings.Join(cmd.Env, "\n")
	if strings.Contains(env, "BatchMode") || !strings.Contains(env, "GIT_SSH_COMMAND=my-ssh -i key") {
		t.Error("the user's own SSH command was overridden")
	}
}
