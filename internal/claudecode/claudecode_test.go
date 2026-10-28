package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/review"
)

// fakeClaude writes a `claude` script that records its arguments, stdin,
// working directory, system prompt file and environment, then prints result.
func fakeClaude(t *testing.T, result string) (bin, rec string) {
	t.Helper()
	bin, rec = t.TempDir(), t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$PLUMBIX_TEST_REC/args"
cat > "$PLUMBIX_TEST_REC/stdin"
pwd > "$PLUMBIX_TEST_REC/cwd"
env > "$PLUMBIX_TEST_REC/env"
prev=""
for a in "$@"; do
  if [ "$prev" = "--system-prompt-file" ]; then cat "$a" > "$PLUMBIX_TEST_REC/system"; fi
  prev="$a"
done
sed "s|WORKTREE|$(pwd)|g" "$PLUMBIX_TEST_REC/result.json"
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "result.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUMBIX_TEST_REC", rec)
	return bin, rec
}

func recorded(t *testing.T, rec, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rec, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const streamed = `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"WORKTREE/app.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"package app"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looking."},{"type":"tool_use","name":"Grep","input":{"pattern":"func Greet","path":"WORKTREE"}},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.0421,"usage":{"input_tokens":1200,"output_tokens":300},"modelUsage":{"claude-haiku-5-5":{"outputTokens":20},"claude-opus-5-5":{"outputTokens":280}},"structured_output":{"outcome":"approve","summary":"Fine."}}
`

// TestRunPassesPromptSchemaPolicyAndEnvironment: Run starts claude in the
// directory with the system prompt in a file, the user prompt on stdin, the
// schema and policy as arguments and the extra environment; tool uses become
// progress lines with paths relative to the directory; the result carries
// usage and the busiest model.
func TestRunPassesPromptSchemaPolicyAndEnvironment(t *testing.T) {
	bin, rec := fakeClaude(t, streamed)
	dir := t.TempDir()
	t.Setenv("PLUMBIX_AI_API_KEY", "raw-secret")
	t.Setenv("ANTHROPIC_API_KEY", "stale")
	var progress []string
	res, err := Run(context.Background(), Options{
		Claude: filepath.Join(bin, "claude"), Dir: dir, System: "You judge.", User: "The change.",
		Schema: review.OutputSchema([]string{"AC-1"}), Tools: review.ToolsReadOnly, Model: "claude-opus-5-5", MaxTurns: 7,
		Env: []string{"ANTHROPIC_API_KEY=sk-ant-test"}, Drop: []string{"PLUMBIX_AI_API_KEY"},
		Progress: func(line string) { progress = append(progress, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NumTurns != 3 || res.Usage.InputTokens != 1200 || res.TotalCostUSD == nil || *res.TotalCostUSD != 0.0421 || res.Model() != "claude-opus-5-5" || !strings.Contains(string(res.StructuredOutput), `"approve"`) {
		t.Errorf("result = %+v", res)
	}
	args := recorded(t, rec, "args")
	for _, want := range []string{"-p\n", "--output-format\nstream-json\n", "--verbose\n", "--json-schema\n", `"AC-1"`, "--system-prompt-file\n", "--no-session-persistence\n", "--permission-prompts\nnone\n", "--max-turns\n7\n", "--restricted\n--tools\nRead,Grep,Glob\n", "--model\nclaude-opus-5-5\n", Task + "\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "Bash") || strings.Contains(args, "--max-budget-usd") {
		t.Errorf("read-only args allow Bash or set a budget:\n%s", args)
	}
	if got := recorded(t, rec, "system"); got != "You judge." {
		t.Errorf("system prompt = %q", got)
	}
	if got := recorded(t, rec, "stdin"); got != "The change." {
		t.Errorf("stdin = %q", got)
	}
	if got := strings.TrimSpace(recorded(t, rec, "cwd")); got != dir && !strings.HasSuffix(got, dir) {
		t.Errorf("cwd = %q, want %q", got, dir)
	}
	if env := recorded(t, rec, "env"); !strings.Contains(env, "ANTHROPIC_API_KEY=sk-ant-test\n") || !strings.Contains(env, "PLUMBIX_TEST_REC=") || strings.Contains(env, "raw-secret") || strings.Contains(env, "stale") {
		t.Errorf("env should carry the extra variable once, the inherited ones, and no dropped or overridden value:\n%s", env)
	}
	want := []string{"turn 1: Read app.go", "turn 2: Grep func Greet", "turn 2: Bash go test ./..."}
	if strings.Join(progress, "|") != strings.Join(want, "|") {
		t.Errorf("progress = %q, want %q", progress, want)
	}
}

// TestFullPolicyAddsBashAndBudget: the full policy allows Bash (never Edit)
// and a budget becomes --max-budget-usd.
func TestFullPolicyAddsBashAndBudget(t *testing.T) {
	bin, rec := fakeClaude(t, streamed)
	if _, err := Run(context.Background(), Options{Claude: filepath.Join(bin, "claude"), Dir: t.TempDir(), Tools: review.ToolsFull, MaxBudgetUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	args := recorded(t, rec, "args")
	if !strings.Contains(args, "--tools\nBash,Read,Grep,Glob\n--allowedTools\nBash,Read,Grep,Glob\n") || strings.Contains(args, "--restricted") || strings.Contains(args, "Edit") || !strings.Contains(args, "--max-budget-usd\n1.50\n") {
		t.Errorf("full args:\n%s", args)
	}
}

// TestFailuresAreErrors: an error result, a result without structured output
// and output that is no result at all are errors naming the cause.
func TestFailuresAreErrors(t *testing.T) {
	cases := map[string]string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Not logged in"}`: "Not logged in",
		`{"type":"result","subtype":"success","is_error":false,"result":"I could not decide."}`:         "no structured output",
		`not json at all`: "no JSON result",
	}
	for result, want := range cases {
		bin, _ := fakeClaude(t, result)
		_, err := Run(context.Background(), Options{Claude: filepath.Join(bin, "claude"), Dir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("result %q: err = %v, want %q", result, err, want)
		}
	}
}

// TestReadStreamAcceptsABareResultAndDrainsTheRest: a plain result object
// with no stream around it is accepted, and whatever follows is read.
func TestReadStreamAcceptsABareResultAndDrainsTheRest(t *testing.T) {
	in := `{"subtype":"success","structured_output":{"outcome":"approve"}}` + "\n" + strings.Repeat("noise\n", 1000)
	res, found, _ := readStream(strings.NewReader(in), "/w", nil)
	if !found || !strings.Contains(string(res.StructuredOutput), "approve") {
		t.Errorf("bare result: found %v, %+v", found, res)
	}
	if _, found, raw := readStream(strings.NewReader("hello\n"), "/w", nil); found || raw != "hello\n" {
		t.Errorf("no result: found %v raw %q", found, raw)
	}
}

func TestDescribeToolRendersPathsRelativeAndCutsLongArguments(t *testing.T) {
	long := strings.Repeat("x", maxProgressArg+5)
	cases := []struct{ name, input, want string }{
		{"Read", `{"file_path":"/w/a/b.go"}`, "Read a/b.go"},
		{"Read", `{"file_path":"/w"}`, "Read ."},
		{"Grep", `{"pattern":"x y","path":"/w/pkg"}`, "Grep x y in pkg"},
		{"Glob", `{"pattern":"**/*.go"}`, "Glob **/*.go"},
		{"Bash", `{"command":"  ls\n -la  "}`, "Bash ls -la"},
		{"Bash", `{"command":"` + long + `"}`, "Bash " + long[:maxProgressArg] + "…"},
		{"WebFetch", `{"url":"https://x"}`, "WebFetch https://x"},
		{"Task", `{}`, "Task"},
	}
	for _, c := range cases {
		if got := describeTool(c.name, []byte(c.input), "/w"); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

// TestVerdictUsageKeepsCostOnlyWhenReported (WU-053 follow-up): a result
// line without total_cost_usd gives no cost, one with 0 gives a reported
// zero; neither is ever defaulted.
func TestVerdictUsageKeepsCostOnlyWhenReported(t *testing.T) {
	var none, zero Result
	if err := json.Unmarshal([]byte(`{"type":"result","num_turns":1,"usage":{"input_tokens":1,"output_tokens":2}}`), &none); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"type":"result","num_turns":1,"total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":2}}`), &zero); err != nil {
		t.Fatal(err)
	}
	if u := none.VerdictUsage("m"); u.CostUSD != nil {
		t.Errorf("unreported cost = %v", *u.CostUSD)
	}
	if u := zero.VerdictUsage("m"); u.CostUSD == nil || *u.CostUSD != 0 {
		t.Errorf("reported zero = %v", u.CostUSD)
	}
	if got := CostText(none.TotalCostUSD); got != "cost not reported" {
		t.Errorf("CostText(nil) = %q", got)
	}
	if got := CostText(zero.TotalCostUSD); got != "$0.00" {
		t.Errorf("CostText(0) = %q", got)
	}
}
