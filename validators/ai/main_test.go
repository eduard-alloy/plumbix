package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

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

func fakeAPI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		text := `{"outcome": "approve", "summary": "tests pass", "criteria": [{"id": "AC-1", "status": "met", "reason": "step passed", "evidence": ["evidence/test/log.txt"]}], "findings": [], "risks": []}`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 500, "output_tokens": 80},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWritesVerdictWithUsage(t *testing.T) {
	in, out, ws := setup(t)
	srv := fakeAPI(t)
	env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws, "ANTHROPIC_API_KEY": "sk-secret-value"}
	var stderr bytes.Buffer
	code := run(func(k string) string { return env[k] }, &stderr, option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	v, err := validation.ReadVerdict(filepath.Join(out, validation.VerdictFile), crit)
	if err != nil || v.Outcome != validation.Approve || v.Usage == nil || v.Usage.InputTokens != 500 {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	if strings.Contains(stderr.String(), "sk-secret-value") {
		t.Fatal("stderr leaks the API key")
	}
	if !strings.Contains(stderr.String(), "500 input") {
		t.Errorf("stderr should report token usage: %s", stderr.String())
	}
}

func TestFailsWithoutVerdict(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"missing key": {},
		"bad effort":  {"ANTHROPIC_API_KEY": "k", "PLUMBIX_AI_EFFORT": "extreme"},
	} {
		t.Run(name, func(t *testing.T) {
			in, out, ws := setup(t)
			env := map[string]string{"PLUMBIX_IN": in, "PLUMBIX_OUT": out, "PLUMBIX_WORKSPACE": ws}
			for k, v := range extra {
				env[k] = v
			}
			if code := run(func(k string) string { return env[k] }, &bytes.Buffer{}); code == 0 {
				t.Fatal("expected failure")
			}
			if _, err := os.Stat(filepath.Join(out, validation.VerdictFile)); err == nil {
				t.Fatal("a verdict was written despite the failure")
			}
		})
	}
}
