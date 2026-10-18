package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eduard-alloy/plumbix/internal/client"
)

// fakeAPI is a Plumbix API that records requests and answers from a script.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string          // "METHOD path" with the raw query
	bodies   map[string]string // last body per "METHOD path"
	events   []string          // SSE frames served by /events, in order
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	record := func(r *http.Request) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		var b strings.Builder
		if r.Body != nil {
			buf := make([]byte, 1<<16)
			n, _ := r.Body.Read(buf)
			b.Write(buf[:n])
		}
		f.bodies[key] = b.String()
		return key
	}
	mux.HandleFunc("GET /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"repos":[{"id":"r_1","host":"github","url":"https://github.com/a/b","protected_branches":["main"],"merge_strategy":"squash","created_at":"2026-10-18T00:00:00Z"}]}`))
	})
	mux.HandleFunc("POST /api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"r_2","host":"github","url":"https://github.com/a/c","protected_branches":["main"],"merge_strategy":"squash","created_at":"2026-10-18T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /api/v1/changes", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"c_1","repo_id":"r_1","source_branch":"feat","target_branch":"main","title":"t","intent":"i","acceptance_criteria":[{"id":"AC-1","text":"x"}],"constraints":[],"state":"running","created_at":"2026-10-18T00:00:00Z","updated_at":"2026-10-18T00:00:00Z"}`))
	})
	mux.HandleFunc("POST /api/v1/changes/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_state","message":"change c_1 is running; a person approves needs_human or escalated changes"}}`))
	})
	mux.HandleFunc("GET /api/v1/changes/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"id":"c_1","repo_id":"r_1","source_branch":"feat","target_branch":"main","title":"t","intent":"i","acceptance_criteria":[],"constraints":[],"state":"running","created_at":"2026-10-18T00:00:00Z","updated_at":"2026-10-18T00:00:00Z"}`))
	})
	mux.HandleFunc("GET /api/v1/changes/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"runs":[{"id":"run_1","repo_id":"r_1","change_id":"c_1","iteration":1,"status":"finished","outcome":"fix_requested","steps":[],"started_at":"2026-10-18T00:00:00Z"},{"id":"run_2","repo_id":"r_1","change_id":"c_1","iteration":2,"status":"running","steps":[],"started_at":"2026-10-18T00:01:00Z"}]}`))
	})
	mux.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		f.mu.Lock()
		frames := append([]string(nil), f.events...)
		f.mu.Unlock()
		for _, fr := range frames {
			time.Sleep(50 * time.Millisecond)
			_, _ = fmt.Fprint(w, fr)
			if fl != nil {
				fl.Flush()
			}
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("GET /api/v1/reviews/next", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func connect(t *testing.T, api *fakeAPI, name string, opts ...func(*Options)) (*mcp.ClientSession, string) {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	o := Options{Client: client.New(strings.TrimPrefix(srv.URL, "http://"), "t"), Name: name, Version: "test"}
	for _, f := range opts {
		f(&o)
	}
	s := New(o)
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = s.Server.Run(context.Background(), st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, s.Identity
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsWrapTheAPIUnderTheSessionIdentity(t *testing.T) {
	api := &fakeAPI{bodies: map[string]string{}}
	cs, identity := connect(t, api, "kai")
	if !strings.HasPrefix(identity, "mcp:kai:") || len(identity) != len("mcp:kai:")+6 {
		t.Fatalf("identity = %q", identity)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, want := range []string{"list_repos", "register_repo", "submit_change", "get_change", "list_changes", "get_runs", "get_step_log", "wait_for_verdict", "merge_change", "refresh_change", "close_change", "validate_change", "next_review", "post_verdict", "list_alerts", "resolve_alert"} {
		if !have[want] {
			t.Errorf("tool %s is missing", want)
		}
	}
	// Approval is a person's act: a plain session has no approve_change.
	if have["approve_change"] {
		t.Error("approve_change is offered without --allow-approve")
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve_change", Arguments: map[string]any{"change_id": "c_1", "reason": "fine"}}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("approve_change without --allow-approve: %v", err)
	}
	// A submission carries the session identity, whatever the agent says.
	res := call(t, cs, "submit_change", map[string]any{"repo_id": "r_1", "source_branch": "feat", "title": "t", "intent": "i",
		"acceptance_criteria": []map[string]any{{"text": "tests pass", "check": map[string]any{"steps": []string{"test"}}}}, "submitted_by": "someone-else"})
	if res.IsError {
		t.Fatalf("submit: %s", text(res))
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(api.bodies["POST /api/v1/changes"]), &sent)
	if sent["submitted_by"] != identity || sent["target_branch"] != "main" {
		t.Errorf("submitted body = %v", sent)
	}
	if !strings.Contains(text(res), `"c_1"`) {
		t.Errorf("submit result = %s", text(res))
	}
	// With --allow-approve the tool exists, and a refusal is a tool error carrying the API's code.
	approver, _ := connect(t, api, "eduard", func(o *Options) { o.AllowApprove = true })
	res = call(t, approver, "approve_change", map[string]any{"change_id": "c_1", "reason": "fine"})
	if !res.IsError || !strings.Contains(text(res), "invalid_state") {
		t.Errorf("approve refusal = %v %s", res.IsError, text(res))
	}
	// A claim polls as the session, so the server's own-change rule applies to it.
	res = call(t, cs, "next_review", map[string]any{"wait_seconds": 0})
	if res.IsError || !strings.Contains(text(res), `"waiting":false`) {
		t.Errorf("idle claim = %v %s", res.IsError, text(res))
	}
	var sawClaim bool
	for _, r := range api.requests {
		if strings.HasPrefix(r, "GET /api/v1/reviews/next?") && strings.Contains(r, "reviewer=mcp%3Akai%3A") && strings.Contains(r, "wait=0") {
			sawClaim = true
		}
	}
	if !sawClaim {
		t.Errorf("requests = %v", api.requests)
	}
	if res := call(t, cs, "list_repos", map[string]any{}); res.IsError || !strings.Contains(text(res), "r_1") {
		t.Errorf("list_repos = %v %s", res.IsError, text(res))
	}
	// Registration carries no token: a repository Plumbix can already reach, or a public one.
	res = call(t, cs, "register_repo", map[string]any{"host": "github", "url": "https://github.com/a/c", "protected_branches": []string{"main", "release"}})
	if res.IsError || !strings.Contains(text(res), `"r_2"`) {
		t.Errorf("register_repo = %v %s", res.IsError, text(res))
	}
	var repoBody map[string]any
	_ = json.Unmarshal([]byte(api.bodies["POST /api/v1/repos"]), &repoBody)
	if repoBody["host"] != "github" || repoBody["url"] != "https://github.com/a/c" || fmt.Sprint(repoBody["protected_branches"]) != "[main release]" || repoBody["merge_strategy"] != nil {
		t.Errorf("registration body = %v", repoBody)
	}
	if _, has := repoBody["token"]; has {
		t.Error("a token field was sent")
	}
}

func TestWaitForVerdictFollowsTheRunningIteration(t *testing.T) {
	api := &fakeAPI{bodies: map[string]string{}}
	api.events = []string{
		"id: 1\nevent: verdict.issued\ndata: {\"id\":1,\"type\":\"verdict.issued\",\"time\":\"2026-10-18T00:00:30Z\",\"repo_id\":\"r_1\",\"change_id\":\"c_1\",\"run_id\":\"run_1\",\"payload\":{\"iteration\":1,\"outcome\":\"fix_requested\"}}\n\n",
		"id: 2\nevent: verdict.issued\ndata: {\"id\":2,\"type\":\"verdict.issued\",\"time\":\"2026-10-18T00:02:30Z\",\"repo_id\":\"r_1\",\"change_id\":\"c_1\",\"run_id\":\"run_2\",\"payload\":{\"iteration\":2,\"outcome\":\"approved\"}}\n\n",
	}
	cs, _ := connect(t, api, "kai")
	// The old verdict is replayed first; the tool waits for the iteration that is running.
	res := call(t, cs, "wait_for_verdict", map[string]any{"change_id": "c_1", "timeout_seconds": 10})
	if res.IsError || !strings.Contains(text(res), `"outcome":"approved"`) || !strings.Contains(text(res), `"iteration":2`) {
		t.Errorf("wait = %v %s", res.IsError, text(res))
	}
	// A final state or an errored run ends the wait too.
	api.mu.Lock()
	api.events = []string{"id: 3\nevent: change.state_changed\ndata: {\"id\":3,\"type\":\"change.state_changed\",\"time\":\"2026-10-18T00:03:00Z\",\"repo_id\":\"r_1\",\"change_id\":\"c_1\",\"payload\":{\"from\":\"running\",\"to\":\"closed\"}}\n\n"}
	api.mu.Unlock()
	res = call(t, cs, "wait_for_verdict", map[string]any{"change_id": "c_1", "timeout_seconds": 10})
	if res.IsError || !strings.Contains(text(res), `"reached":"closed"`) {
		t.Errorf("closed = %v %s", res.IsError, text(res))
	}
	api.mu.Lock()
	api.events = []string{"id: 4\nevent: run.finished\ndata: {\"id\":4,\"type\":\"run.finished\",\"time\":\"2026-10-18T00:03:00Z\",\"repo_id\":\"r_1\",\"change_id\":\"c_1\",\"run_id\":\"run_2\",\"payload\":{\"iteration\":2,\"status\":\"errored\",\"error\":\"no pipeline\"}}\n\n"}
	api.mu.Unlock()
	res = call(t, cs, "wait_for_verdict", map[string]any{"change_id": "c_1", "timeout_seconds": 10})
	if res.IsError || !strings.Contains(text(res), `"reached":"errored"`) {
		t.Errorf("errored = %v %s", res.IsError, text(res))
	}
	// Nothing arrives in time: the tool says so instead of hanging.
	api.mu.Lock()
	api.events = nil
	api.mu.Unlock()
	start := time.Now()
	res = call(t, cs, "wait_for_verdict", map[string]any{"change_id": "c_1", "timeout_seconds": 1})
	if res.IsError || !strings.Contains(text(res), `"timed_out":true`) || time.Since(start) > 5*time.Second {
		t.Errorf("timeout = %v %s after %s", res.IsError, text(res), time.Since(start))
	}
}
