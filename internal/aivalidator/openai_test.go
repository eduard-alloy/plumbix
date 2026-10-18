package aivalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

// fakeOpenAI is a chat-completions endpoint that records requests and can
// refuse response_format kinds, as some vendors do.
type fakeOpenAI struct {
	progress    bytes.Buffer // what the validator reported while it worked
	mu          sync.Mutex
	requests    []map[string]any
	auth        []string
	rejectKinds map[string]bool // response_format types answered with 400
	status      int
	content     string
}

func (f *fakeOpenAI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
			return
		}
		if rf, ok := req["response_format"].(map[string]any); ok && f.rejectKinds[rf["type"].(string)] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid parameter: 'response_format' of type '` + rf["type"].(string) + `' is not supported with this model.","type":"invalid_request_error","param":"response_format"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-5.5",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": f.content}}},
			"usage":   map[string]any{"prompt_tokens": 800, "completion_tokens": 150},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runOpenAI(t *testing.T, f *fakeOpenAI, key string) (validation.Verdict, error) {
	t.Helper()
	fx := newFixture(t, nil, []validation.StepEvidence{{Step: "test", Status: "succeeded"}})
	srv := f.server(t)
	cfg := Config{Provider: ProviderOpenAI, BaseURL: srv.URL + "/v1", APIKey: key, Model: "gpt-5.5", Effort: DefaultEffort, Progress: &f.progress}
	return Validate(context.Background(), cfg, fx.in, fx.ws)
}

func TestOpenAICompatibleRequestAndVerdict(t *testing.T) {
	f := &fakeOpenAI{content: goodOutput, rejectKinds: map[string]bool{}}
	v, err := runOpenAI(t, f, "sk-vendor")
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != "fix" || v.Usage == nil || v.Usage.Model != "gpt-5.5" || v.Usage.InputTokens != 800 || v.Usage.OutputTokens != 150 {
		t.Errorf("verdict = %+v usage %+v", v.Outcome, v.Usage)
	}
	if len(f.requests) != 1 || f.auth[0] != "Bearer sk-vendor" {
		t.Fatalf("requests %d, auth %v", len(f.requests), f.auth)
	}
	req := f.requests[0]
	msgs, _ := req["messages"].([]any)
	if req["model"] != "gpt-5.5" || len(msgs) != 2 {
		t.Fatalf("request = %v", req)
	}
	sys, _ := msgs[0].(map[string]any)
	user, _ := msgs[1].(map[string]any)
	if sys["role"] != "system" || !strings.Contains(sys["content"].(string), "independent validator") || user["role"] != "user" || !strings.Contains(user["content"].(string), "Add retries") {
		t.Errorf("messages = %v", msgs)
	}
	if !strings.Contains(user["content"].(string), `"AC-1"`) || !strings.Contains(user["content"].(string), "single JSON object") {
		t.Errorf("the user prompt does not end with the schema and the JSON instruction")
	}
	rf, _ := req["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", rf)
	}
	if js, _ := rf["json_schema"].(map[string]any); js == nil || js["name"] != "plumbix_verdict" || js["schema"] == nil {
		t.Errorf("json_schema = %v", rf["json_schema"])
	}
	if _, has := req["temperature"]; has {
		t.Error("temperature was set; vendors reject it on reasoning models")
	}
	// The progress line names the model, the host, the prompt size and the
	// format, and never the key (WU-031).
	progress := f.progress.String()
	if !strings.Contains(progress, "ai validator: asking gpt-5.5 at 127.0.0.1:") || !strings.Contains(progress, "characters of prompt, response_format json_schema") {
		t.Errorf("progress = %q", progress)
	}
	if strings.Contains(progress, "sk-vendor") {
		t.Errorf("the key reached the progress output: %q", progress)
	}
}

func TestOpenAICompatibleFallsBackWhenAVendorRejectsTheFormat(t *testing.T) {
	// json_schema refused, json_object accepted.
	f := &fakeOpenAI{content: "```json\n" + goodOutput + "\n```", rejectKinds: map[string]bool{"json_schema": true}}
	if _, err := runOpenAI(t, f, "k"); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[1]["response_format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("requests = %v", f.requests)
	}
	if p := f.progress.String(); !strings.Contains(p, "refused response_format json_schema; retrying with json_object") {
		t.Errorf("fallback progress = %q", p)
	}
	// Both refused: a plain request, and the prompt's instruction has to carry it.
	f = &fakeOpenAI{content: goodOutput, rejectKinds: map[string]bool{"json_schema": true, "json_object": true}}
	if _, err := runOpenAI(t, f, "k"); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	if _, has := f.requests[2]["response_format"]; has {
		t.Error("the third request still carried response_format")
	}
}

func TestOpenAICompatibleFailuresAreClear(t *testing.T) {
	fx := newFixture(t, nil, nil)
	_, err := Validate(context.Background(), Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:1/v1", Model: "m", Effort: DefaultEffort}, fx.in, fx.ws)
	if err == nil || !strings.Contains(err.Error(), "PLUMBIX_AI_API_KEY") || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("missing key: %v", err)
	}
	_, err = Validate(context.Background(), Config{Provider: ProviderOpenAI, APIKey: "k", Model: "m", Effort: DefaultEffort}, fx.in, fx.ws)
	if err == nil || !strings.Contains(err.Error(), "PLUMBIX_AI_BASE_URL") {
		t.Errorf("missing base URL: %v", err)
	}
	f := &fakeOpenAI{status: http.StatusUnauthorized, rejectKinds: map[string]bool{}}
	_, err = runOpenAI(t, f, "bad")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("401: %v", err)
	}
	f = &fakeOpenAI{content: "I cannot review this.", rejectKinds: map[string]bool{}}
	_, err = runOpenAI(t, f, "k")
	var inv *validation.InvalidVerdictError
	if err == nil || (!errors.As(err, &inv) && !strings.Contains(err.Error(), "not valid JSON")) {
		t.Errorf("prose answer: %v", err)
	}
	_, err = Validate(context.Background(), Config{Provider: "bedrock", APIKey: "k", Model: "m", Effort: DefaultEffort}, fx.in, fx.ws)
	if err == nil || !strings.Contains(err.Error(), "anthropic or openai") {
		t.Errorf("unknown provider: %v", err)
	}
}
