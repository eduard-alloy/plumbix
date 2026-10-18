package aivalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

const goodOutput = `{"outcome": "fix", "summary": "README not updated",
  "criteria": [
    {"id": "AC-1", "status": "met", "reason": "test passed", "evidence": ["evidence/test/log.txt"]},
    {"id": "AC-2", "status": "unmet", "reason": "README unchanged", "evidence": []}],
  "findings": [{"severity": "major", "message": "Document retries in README", "criterion": "AC-2", "file": "README.md", "line": 0}],
  "risks": []}`

// fakeAPI serves one canned Messages API response and records the request.
type fakeAPI struct {
	progress   bytes.Buffer
	status     int
	stopReason string
	text       string
	body       map[string]any
	header     http.Header
	path       string
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.body)
		f.header, f.path = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"type": "error", "error": {"type": "api_error", "message": "boom"}}`))
			return
		}
		resp := map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content":     []map[string]any{{"type": "text", "text": f.text}},
			"stop_reason": f.stopReason, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 900, "output_tokens": 120},
		}
		if f.stopReason == "refusal" {
			resp["stop_details"] = map[string]any{"type": "refusal", "category": "cyber", "explanation": "declined"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runFake(t *testing.T, f *fakeAPI) (validation.Verdict, error) {
	t.Helper()
	fx := newFixture(t, nil, []validation.StepEvidence{{Step: "test", Status: "succeeded"}})
	srv := f.server(t)
	cfg := Config{APIKey: "sk-test", Model: DefaultModel, Effort: DefaultEffort, Progress: &f.progress}
	return Validate(context.Background(), cfg, fx.in, fx.ws, option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
}

func TestRequestShape(t *testing.T) {
	f := &fakeAPI{stopReason: "end_turn", text: goodOutput}
	v, err := runFake(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != validation.Fix || v.Usage.Model != "claude-opus-5-5" || v.Usage.InputTokens != 900 || v.Usage.OutputTokens != 120 {
		t.Errorf("verdict = %+v usage %+v", v, v.Usage)
	}
	if f.path != "/v1/messages" {
		t.Errorf("path = %s", f.path)
	}
	if f.header.Get("X-Api-Key") != "sk-test" || !strings.Contains(f.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("headers: key=%q beta=%q", f.header.Get("X-Api-Key"), f.header.Get("Anthropic-Beta"))
	}
	b := f.body
	if b["model"] != "claude-opus-5-5" || b["max_tokens"] != float64(MaxTokens) || b["fallbacks"] != "default" {
		t.Errorf("model %v max_tokens %v fallbacks %v", b["model"], b["max_tokens"], b["fallbacks"])
	}
	oc, _ := b["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	schema, _ := format["schema"].(map[string]any)
	if oc["effort"] != "high" || format["type"] != "json_schema" || schema["type"] != "object" {
		t.Errorf("output_config = %v", oc)
	}
	system := b["system"].([]any)[0].(map[string]any)["text"].(string)
	msg := b["messages"].([]any)[0].(map[string]any)
	userText := msg["content"].([]any)[0].(map[string]any)["text"].(string)
	if msg["role"] != "user" || !strings.Contains(system, "untrusted") || !strings.Contains(userText, "<untrusted-") {
		t.Error("system prompt or untrusted block missing from the request")
	}
	if _, set := b["thinking"]; set {
		t.Error("thinking should be left unset on Opus 5.5 (always adaptive)")
	}
	// The progress line names the model and effort and the prompt size, never the key (WU-031).
	if p := f.progress.String(); !strings.Contains(p, "ai validator: asking claude-opus-5-5 through the Anthropic API (effort high): ") || !strings.Contains(p, " characters of prompt") || strings.Contains(p, "sk-test") {
		t.Errorf("progress = %q", p)
	}
}

func TestFailures(t *testing.T) {
	cases := map[string]*fakeAPI{
		"refusal":      {stopReason: "refusal", text: ""},
		"max tokens":   {stopReason: "max_tokens", text: `{"outcome": "fix", "summ`},
		"server error": {status: http.StatusInternalServerError},
		"unauthorized": {status: http.StatusUnauthorized},
		"bad output":   {stopReason: "end_turn", text: `{"outcome": "shrug", "summary": "s", "criteria": [], "findings": [], "risks": []}`},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := runFake(t, f); err == nil {
				t.Fatal("expected an error, got a verdict")
			}
		})
	}
}

func TestMissingKey(t *testing.T) {
	fx := newFixture(t, nil, nil)
	_, err := Validate(context.Background(), Config{Model: DefaultModel, Effort: DefaultEffort}, fx.in, fx.ws)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusalErrorExplains(t *testing.T) {
	_, err := runFake(t, &fakeAPI{stopReason: "refusal"})
	if err == nil || !strings.Contains(err.Error(), "refus") || !strings.Contains(err.Error(), "cyber") {
		t.Fatalf("err = %v", err)
	}
}
