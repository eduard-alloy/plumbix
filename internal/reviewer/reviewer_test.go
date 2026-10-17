package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/gitops/gittest"
	"github.com/eduard-alloy/plumbix/internal/review"
)

// fakeClaude puts a `claude` on the PATH that records how it was called
// and prints result, as claude -p --output-format json would.
func fakeClaude(t *testing.T, result string) (rec string) {
	t.Helper()
	bin, rec := t.TempDir(), t.TempDir()
	// The worktree and the prompt file are gone once the review ends, so the
	// script records what it saw while it ran.
	script := `#!/bin/sh
printf '%s\n' "$@" > "$PLUMBIX_TEST_REC/args"
cat > "$PLUMBIX_TEST_REC/stdin"
pwd > "$PLUMBIX_TEST_REC/cwd"
ls > "$PLUMBIX_TEST_REC/ls"
prev=""
for a in "$@"; do
  if [ "$prev" = "--system-prompt-file" ]; then cat "$a" > "$PLUMBIX_TEST_REC/system"; fi
  prev="$a"
done
cat "$PLUMBIX_TEST_REC/result.json"
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "result.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PLUMBIX_TEST_REC", rec)
	return rec
}

func recorded(t *testing.T, rec, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rec, name))
	if err != nil {
		t.Fatalf("fake claude left no %s: %v", name, err)
	}
	return string(b)
}

// fakeServer serves one brief, then nothing, and keeps what was posted.
type fakeServer struct {
	mu     sync.Mutex
	brief  review.Brief
	claims int
	posted struct {
		Token   string          `json:"token"`
		Verdict json.RawMessage `json:"verdict"`
	}
	postedID string
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/reviews/next", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.claims++
		if f.claims > 1 || r.URL.Query().Get("reviewer") == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.brief)
	})
	mux.HandleFunc("POST /api/v1/reviews/{id}/verdict", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.postedID = r.PathValue("id")
		_ = json.NewDecoder(r.Body).Decode(&f.posted)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted","review_id":"` + f.postedID + `"}`))
	})
	return mux
}

const approving = `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.0421,
 "usage":{"input_tokens":1200,"output_tokens":300},
 "structured_output":{"outcome":"approve","summary":"The feature does what the intent says.",
   "criteria":[{"id":"AC-1","status":"met","reason":"app.go defines the package","evidence":["app.go:1"]}],"findings":[],"risks":[]}}`

func newBrief(repoURL, head, base string) review.Brief {
	ids := []string{"AC-1"}
	return review.Brief{ID: "rv_1", RunID: "run_1", ChangeID: "c_1", RepoID: "r_1", RepoURL: repoURL, Step: "ai-review", Iteration: 1, Head: head, Base: base,
		Token: "tok-1", Tools: review.ToolsReadOnly, Prompt: review.Prompt{System: "SYSTEM RULES", User: "USER PROMPT mentioning app.go"}, Schema: review.OutputSchema(ids), Criteria: ids}
}

func TestOneReviewRunsClaudeInAWorktreeAndPostsTheVerdict(t *testing.T) {
	repo := gittest.New(t)
	base := repo.Commit("main", map[string]string{"README.md": "# base\n"}, "base")
	head := repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	rec := fakeClaude(t, approving)
	fs := &fakeServer{brief: newBrief(repo.Remote, head, base)}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	var out, errOut bytes.Buffer
	work := t.TempDir()
	err := Run(context.Background(), Options{Client: client.New(strings.TrimPrefix(srv.URL, "http://"), "t"), Reviewer: "kai", Once: true, WorkDir: work, Stdout: &out, Stderr: &errOut})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut.String())
	}
	// The verdict is the model's output in the validator contract's shape.
	if fs.postedID != "rv_1" || fs.posted.Token != "tok-1" {
		t.Fatalf("posted to %q with token %q", fs.postedID, fs.posted.Token)
	}
	var v map[string]any
	if err := json.Unmarshal(fs.posted.Verdict, &v); err != nil {
		t.Fatal(err)
	}
	if v["plumbix"] != "verdict/v1" || v["outcome"] != "approve" || !strings.Contains(string(fs.posted.Verdict), `"AC-1"`) {
		t.Errorf("verdict = %s", fs.posted.Verdict)
	}
	if usage, _ := v["usage"].(map[string]any); usage == nil || usage["input_tokens"] != float64(1200) {
		t.Errorf("verdict usage = %v", v["usage"])
	}
	// claude ran non-interactively, read-only, with the brief's prompt and schema, in a checkout of head.
	args := recorded(t, rec, "args")
	for _, want := range []string{"-p", "--output-format\njson", "--json-schema", "--system-prompt-file", "--restricted", "--no-session-persistence", "--max-turns", "--permission-prompts\nnone"} {
		if !strings.Contains(args, want) {
			t.Errorf("claude args lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--allowedTools") || strings.Contains(args, "Bash") {
		t.Errorf("read-only review allowed Bash:\n%s", args)
	}
	lines := strings.Split(strings.TrimSpace(args), "\n")
	for i, a := range lines {
		if a == "--json-schema" && !strings.Contains(lines[i+1], `"AC-1"`) {
			t.Errorf("schema = %s", lines[i+1])
		}
	}
	if sys := recorded(t, rec, "system"); sys != "SYSTEM RULES" {
		t.Errorf("system prompt file = %q", sys)
	}
	if got := recorded(t, rec, "stdin"); got != "USER PROMPT mentioning app.go" {
		t.Errorf("stdin = %q", got)
	}
	cwd := strings.TrimSpace(recorded(t, rec, "cwd"))
	if ls := recorded(t, rec, "ls"); !strings.Contains(ls, "app.go") || !strings.Contains(ls, "README.md") || !strings.Contains(cwd, filepath.Base(work)) {
		t.Errorf("claude ran in %s seeing %q; want a checkout of head under %s", cwd, ls, work)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("the worktree %s was left behind", cwd)
	}
	if !strings.Contains(out.String(), "rv_1") || !strings.Contains(out.String(), "approve") || !strings.Contains(out.String(), "$0.04") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestFullToolsAllowBashAndAFailedRunPostsNothing(t *testing.T) {
	repo := gittest.New(t)
	base := repo.Commit("main", map[string]string{"README.md": "# base\n"}, "base")
	head := repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	rec := fakeClaude(t, approving)
	b := newBrief(repo.Remote, head, base)
	b.Tools = review.ToolsFull
	b.Model = "claude-opus-5-5"
	fs := &fakeServer{brief: b}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), Options{Client: client.New(strings.TrimPrefix(srv.URL, "http://"), "t"), Reviewer: "kai", Once: true, WorkDir: t.TempDir(), Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatal(err)
	}
	args := recorded(t, rec, "args")
	if !strings.Contains(args, "Bash") || strings.Contains(args, "--restricted") || !strings.Contains(args, "--model\nclaude-opus-5-5") {
		t.Errorf("full-tools args:\n%s", args)
	}
	if strings.Contains(args, "Edit") || strings.Contains(args, "Write") {
		t.Errorf("a reviewer may not edit:\n%s", args)
	}

	// A run that errors or returns no structured output posts nothing: the step times out instead.
	rec = fakeClaude(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Not logged in"}`)
	fs2 := &fakeServer{brief: newBrief(repo.Remote, head, base)}
	srv2 := httptest.NewServer(fs2.handler())
	defer srv2.Close()
	out.Reset()
	errOut.Reset()
	err := Run(context.Background(), Options{Client: client.New(strings.TrimPrefix(srv2.URL, "http://"), "t"), Reviewer: "kai", Once: true, WorkDir: t.TempDir(), Stdout: &out, Stderr: &errOut})
	if err == nil || fs2.postedID != "" || !strings.Contains(errOut.String(), "Not logged in") {
		t.Errorf("failed run: err %v, posted %q, stderr %q", err, fs2.postedID, errOut.String())
	}
	_ = rec
}
