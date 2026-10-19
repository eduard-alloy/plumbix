package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var crit = []change.Criterion{{ID: "AC-1", Text: "tests pass", Check: &change.Check{Steps: []string{"test"}}}}

func setup(t *testing.T) (in, out, ws string) {
	t.Helper()
	in, out, ws = filepath.Join(t.TempDir(), "in"), t.TempDir(), t.TempDir()
	c := change.Change{ID: "c_aaaaaaaaaaaa", Title: "t", Intent: "i", SourceBranch: "f", TargetBranch: "main", AcceptanceCriteria: crit}
	if err := validation.PrepareInputs(in, validation.NewInput(c, "b", "h", nil, 1, nil), []validation.StepEvidence{{Step: "test", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	return in, out, ws
}

const approving = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"WORKSPACE/go.mod"}}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.0421,"usage":{"input_tokens":1200,"output_tokens":300},"modelUsage":{"claude-opus-5-5":{"outputTokens":280}},"structured_output":{"outcome":"approve","summary":"tests pass","criteria":[{"id":"AC-1","status":"met","reason":"step passed","evidence":["evidence/test/log.txt"]}],"findings":[],"risks":[]}}
`

// fakeClaude is a `claude` that records its arguments, stdin, working
// directory and environment and prints result; WORKSPACE in result is the
// directory it ran in.
func fakeClaude(t *testing.T, result string) (exe, rec string) {
	t.Helper()
	bin, rec := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$PLUMBIX_TEST_REC/args"
cat > "$PLUMBIX_TEST_REC/stdin"
pwd > "$PLUMBIX_TEST_REC/cwd"
env > "$PLUMBIX_TEST_REC/env"
sed "s|WORKSPACE|$(pwd)|g" "$PLUMBIX_TEST_REC/result.json"
`
	exe = filepath.Join(bin, "claude")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "result.json"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUMBIX_TEST_REC", rec)
	return exe, rec
}

func recorded(t *testing.T, rec, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rec, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWritesVerdictFromClaudeCode: claude runs in the workspace, read-only
// by default, with the prompt built from the inputs and the schema naming
// the criteria; an API key reaches it as ANTHROPIC_API_KEY; the verdict
// carries the usage; progress and the final line name no secret.
func TestWritesVerdictFromClaudeCode(t *testing.T) {
	in, out, ws := setup(t)
	exe, rec := fakeClaude(t, approving)
	env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws, "PLUMBIX_CLAUDE": exe, "PLUMBIX_AI_API_KEY": "sk-ant-api03-secret-value", "PLUMBIX_AI_MODEL": "claude-opus-5-5"}
	t.Setenv("PLUMBIX_AI_API_KEY", env["PLUMBIX_AI_API_KEY"]) // as the container has it
	var stderr bytes.Buffer
	if code := run(func(k string) string { return env[k] }, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	v, err := validation.ReadVerdict(filepath.Join(out, validation.VerdictFile), crit)
	if err != nil || v.Outcome != validation.Approve || v.Usage == nil || v.Usage.InputTokens != 1200 || v.Usage.Turns != 3 || v.Usage.CostUSD != 0.0421 || v.Usage.Model != "claude-opus-5-5" {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	args := recorded(t, rec, "args")
	for _, want := range []string{"-p\n", "--restricted\n--tools\nRead,Grep,Glob\n", "--model\nclaude-opus-5-5\n", "--max-turns\n40\n", `"AC-1"`} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	if stdin := recorded(t, rec, "stdin"); !strings.Contains(stdin, "c_aaaaaaaaaaaa") || !strings.Contains(stdin, "AC-1: tests pass") {
		t.Errorf("stdin is not the review prompt:\n%s", stdin)
	}
	if cwd := strings.TrimSpace(recorded(t, rec, "cwd")); cwd != ws && !strings.HasSuffix(cwd, ws) {
		t.Errorf("claude ran in %q, want the workspace %q", cwd, ws)
	}
	if e := recorded(t, rec, "env"); !strings.Contains(e, "ANTHROPIC_API_KEY=sk-ant-api03-secret-value\n") || strings.Contains(e, "CLAUDE_CODE_OAUTH_TOKEN=") || strings.Contains(e, "PLUMBIX_AI_API_KEY=") {
		t.Errorf("an API key should reach claude as ANTHROPIC_API_KEY only, never as PLUMBIX_AI_API_KEY:\n%s", e)
	}
	log := stderr.String()
	if strings.Contains(log, "secret-value") {
		t.Fatal("stderr leaks the key")
	}
	if !strings.Contains(log, "claude code validator: turn 1: Read go.mod") || !strings.Contains(log, "claude code validator: approve") || !strings.Contains(log, "3 turns") {
		t.Errorf("stderr lacks the progress or the final line:\n%s", log)
	}
}

// TestTokenToolsAndTurns: a setup-token value reaches claude as
// CLAUDE_CODE_OAUTH_TOKEN, the full policy adds Bash, the turn limit and the
// model's absence are honoured.
func TestTokenToolsAndTurns(t *testing.T) {
	in, out, ws := setup(t)
	exe, rec := fakeClaude(t, approving)
	env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws, "PLUMBIX_CLAUDE": exe, "PLUMBIX_AI_API_KEY": "sk-ant-oat01-token-value", "PLUMBIX_AI_TOOLS": "full", "PLUMBIX_AI_MAX_TURNS": "12"}
	t.Setenv("PLUMBIX_AI_API_KEY", env["PLUMBIX_AI_API_KEY"]) // as the container has it
	var stderr bytes.Buffer
	if code := run(func(k string) string { return env[k] }, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if e := recorded(t, rec, "env"); !strings.Contains(e, "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-token-value\n") || strings.Contains(e, "ANTHROPIC_API_KEY=") || strings.Contains(e, "PLUMBIX_AI_API_KEY=") {
		t.Errorf("a setup token should reach claude as CLAUDE_CODE_OAUTH_TOKEN only, never as PLUMBIX_AI_API_KEY:\n%s", e)
	}
	args := recorded(t, rec, "args")
	if !strings.Contains(args, "--tools\nBash,Read,Grep,Glob\n") || strings.Contains(args, "--restricted") || !strings.Contains(args, "--max-turns\n12\n") || strings.Contains(args, "--model") {
		t.Errorf("args:\n%s", args)
	}
	if v, err := validation.ReadVerdict(filepath.Join(out, validation.VerdictFile), crit); err != nil || v.Usage.Model != "claude-opus-5-5" {
		t.Errorf("without a configured model the busiest one is recorded: %+v %v", v, err)
	}
}

// TestFailsWithoutVerdict: no token, a bad policy, a bad turn limit or a
// claude that returns no verdict all exit non-zero, explain why on stderr,
// and write nothing.
func TestFailsWithoutVerdict(t *testing.T) {
	exe, _ := fakeClaude(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Not logged in"}`)
	for name, c := range map[string]struct {
		extra map[string]string
		want  string
	}{
		"missing token": {map[string]string{}, "PLUMBIX_AI_API_KEY"},
		"bad tools":     {map[string]string{"PLUMBIX_AI_API_KEY": "k", "PLUMBIX_AI_TOOLS": "all"}, "PLUMBIX_AI_TOOLS"},
		"bad turns":     {map[string]string{"PLUMBIX_AI_API_KEY": "k", "PLUMBIX_AI_MAX_TURNS": "many"}, "PLUMBIX_AI_MAX_TURNS"},
		"claude failed": {map[string]string{"PLUMBIX_AI_API_KEY": "k"}, "Not logged in"},
	} {
		t.Run(name, func(t *testing.T) {
			in, out, ws := setup(t)
			env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws, "PLUMBIX_CLAUDE": exe}
			for k, v := range c.extra {
				env[k] = v
			}
			var stderr bytes.Buffer
			if code := run(func(k string) string { return env[k] }, &stderr); code == 0 {
				t.Fatal("expected failure")
			}
			if !strings.Contains(stderr.String(), c.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr.String(), c.want)
			}
			if _, err := os.Stat(filepath.Join(out, validation.VerdictFile)); err == nil {
				t.Fatal("verdict written on failure")
			}
		})
	}
}
