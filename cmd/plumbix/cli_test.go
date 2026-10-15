package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
)

// startServer runs plumbix serve in-process and returns its address and data dir.
func startServer(t *testing.T) (addr, dataDir string) {
	t.Helper()
	clearPlumbixEnv(t)
	dataDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, io.Discard, logs)
	}()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	return waitForListenAddr(t, logs), dataDir
}

type cli struct {
	t               *testing.T
	server, dataDir string
}

// run executes a plumbix client command and returns exit code, stdout, stderr.
func (c cli) run(stdin string, args ...string) (int, string, string) {
	c.t.Helper()
	var out, errOut bytes.Buffer
	full := append(append([]string{}, args...), "--server", c.server, "--data-dir", c.dataDir)
	code := run(full, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (c cli) mustRun(stdin string, args ...string) map[string]any {
	c.t.Helper()
	code, out, errOut := c.run(stdin, args...)
	if code != 0 {
		c.t.Fatalf("plumbix %v: exit %d, stderr %s", args, code, errOut)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		c.t.Fatalf("plumbix %v: stdout is not JSON: %s", args, out)
	}
	return v
}

// redirectGitHub makes git fetch the repo URL from a local test repository,
// so the server's validation runs never touch the network.
func redirectGitHub(t *testing.T, url string) *gittest.Repo {
	t.Helper()
	repo := gittest.New(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo.Remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", url)
	return repo
}

// waitState polls a change until it leaves the running state.
func (c cli) waitState(id string) string {
	c.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st, _ := c.mustRun("", "change", "show", id)["state"].(string); st != "running" && st != "open" {
			return st
		}
	}
	c.t.Fatalf("change %s never left running", id)
	return ""
}

func TestCLIFlow(t *testing.T) {
	repo := redirectGitHub(t, "https://github.com/eduard-alloy/plumbix")
	repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature without a pipeline file")
	addr, dataDir := startServer(t)
	c := cli{t: t, server: addr, dataDir: dataDir}

	registered := c.mustRun("", "repo", "add", "--host", "github", "--url", "https://github.com/eduard-alloy/plumbix", "--protected", "main", "--protected", "release")
	repoID, _ := registered["id"].(string)
	if !strings.HasPrefix(repoID, "r_") || len(registered["protected_branches"].([]any)) != 2 {
		t.Fatalf("repo = %v", registered)
	}
	if repos := c.mustRun("", "repo", "list")["repos"].([]any); len(repos) != 1 {
		t.Errorf("repo list = %v", repos)
	}

	changeJSON := `{"repo_id": "` + repoID + `", "source_branch": "feat", "target_branch": "main", "title": "T", "intent": "I", "acceptance_criteria": [{"text": "works"}]}`
	file := filepath.Join(t.TempDir(), "change.json")
	if err := os.WriteFile(file, []byte(changeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := c.mustRun("", "submit", "-f", file)
	id := ch["id"].(string)
	if ch["state"] != "running" {
		t.Errorf("submitted = %v; submitting should start validation", ch)
	}
	if st := c.waitState(id); st != "needs_human" {
		t.Errorf("state = %s; a branch without a pipeline file needs a human", st)
	}
	runs := c.mustRun("", "change", "runs", id)["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	runID := runs[0].(map[string]any)["id"].(string)
	shown := c.mustRun("", "run", "show", runID)
	if shown["status"] != "errored" || !strings.Contains(shown["error"].(string), "pipeline.yaml") {
		t.Errorf("run = %v", shown)
	}
	if code, _, errOut := c.run("", "run", "log", runID, "test"); code != 1 || !strings.Contains(errOut, "not_found") {
		t.Errorf("log of a step that never ran: exit %d, %q", code, errOut)
	}
	revalidated := c.mustRun("", "change", "validate", id)
	if revalidated["iteration"] != float64(2) {
		t.Errorf("revalidate = %v", revalidated)
	}
	c.waitState(id)
	if code, _, errOut := c.run("", "submit", "-f", file); code != 1 || !strings.Contains(errOut, "conflict") {
		t.Errorf("duplicate submit: exit %d, stderr %q", code, errOut)
	}

	if got := c.mustRun("", "change", "show", id); got["id"] != id {
		t.Errorf("show = %v", got)
	}
	if list := c.mustRun("", "change", "list", "--state", "needs_human", "--repo", repoID)["changes"].([]any); len(list) != 1 {
		t.Errorf("list = %v", list)
	}
	if closed := c.mustRun("", "change", "close", id); closed["state"] != "closed" {
		t.Errorf("close = %v", closed)
	}
	if code, _, errOut := c.run("", "change", "close", id); code != 1 || !strings.Contains(errOut, "invalid_state") {
		t.Errorf("close twice: exit %d, stderr %q", code, errOut)
	}

	stdinChange := strings.Replace(changeJSON, `"feat"`, `"feat-2"`, 1)
	if got := c.mustRun(stdinChange, "submit", "-f", "-"); got["source_branch"] != "feat-2" {
		t.Errorf("submit from stdin = %v", got)
	}
}

func TestCLIWithoutServerToken(t *testing.T) {
	clearPlumbixEnv(t)
	c := cli{t: t, server: "127.0.0.1:1", dataDir: t.TempDir()}
	code, _, errOut := c.run("", "repo", "list")
	if code != 1 || !strings.Contains(errOut, "plumbix serve") {
		t.Fatalf("exit %d, stderr %q; want a hint to start the server", code, errOut)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	clearPlumbixEnv(t)
	c := cli{t: t, server: "127.0.0.1:1", dataDir: t.TempDir()}
	for _, args := range [][]string{
		{"repo"},
		{"repo", "bogus"},
		{"change", "show"},
		{"change", "close"},
		{"submit"},
		{"change", "nope"},
	} {
		if code, _, _ := c.run("", args...); code != 2 {
			t.Errorf("plumbix %v: exit %d, want 2", args, code)
		}
	}
}
