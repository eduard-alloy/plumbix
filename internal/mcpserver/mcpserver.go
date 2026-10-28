// Package mcpserver is plumbix mcp: an MCP server over stdio that lets an
// agent session drive Plumbix through tools (WU-024). Each tool is a thin
// wrapper over the HTTP API, called with the same token and server as
// every client command. The session has an identity, used as submitted_by
// for the changes it submits and as the reviewer name when it claims
// reviews, so the rule that nobody reviews their own change holds for it.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eduard-alloy/plumbix/internal/client"
)

// Limits.
const (
	MaxWait = 55 * time.Minute // a pipeline's worth of waiting for a verdict
	// DefaultWait stays under the tool-call timeouts MCP clients apply by
	// default; the tool says "call again" when it runs out.
	DefaultWait    = 4 * time.Minute
	MaxClaimWait   = 55 // seconds, the API's own cap on GET /reviews/next
	DefaultVersion = "dev"
)

// Options configure the server.
type Options struct {
	Client  *client.Client
	Name    string // the agent's name in the identity; "agent" by default
	Version string
	// AllowApprove registers approve_change. Off by default: approval is a
	// person's act (A23), and an agent session must not move a needs_human
	// or escalated change on its own unless the operator said so.
	AllowApprove bool
}

// Server is the MCP server and the identity it acts under.
type Server struct {
	Server       *mcp.Server
	Identity     string
	c            *client.Client
	allowApprove bool
}

// New builds the server with every tool registered.
func New(o Options) *Server {
	if o.Name == "" {
		o.Name = "agent"
	}
	if o.Version == "" {
		o.Version = DefaultVersion
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	s := &Server{Identity: "mcp:" + o.Name + ":" + hex.EncodeToString(suffix), c: o.Client, allowApprove: o.AllowApprove}
	s.Server = mcp.NewServer(&mcp.Implementation{Name: "plumbix", Version: o.Version}, &mcp.ServerOptions{
		Instructions: "Plumbix validates agent-written changes against their intent and acceptance criteria and merges them. " +
			"Submit a change with submit_change, follow it with wait_for_verdict, read get_runs for what the validators said, " +
			"then merge_change (approved) or refresh_change (target_moved on a branch Plumbix created); a needs_human or escalated change waits for a person unless this session was started with --allow-approve. " +
			"As a reviewer, next_review hands you a brief whose prompt says what to judge; answer with post_verdict. " +
			"This session acts as " + s.Identity + " and never receives its own changes to review.",
	})
	s.register()
	return s
}

// Run serves over stdio until the client disconnects.
func (s *Server) Run(ctx context.Context) error {
	return s.Server.Run(ctx, &mcp.StdioTransport{})
}

// api calls the Plumbix API and returns the decoded JSON body. A refusal
// becomes an error that carries the API's code and message, so the agent
// reads "target_moved: ..." instead of a bare failure.
func (s *Server) api(ctx context.Context, method, path string, body any) (any, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	var resp client.Response
	var err error
	if reader != nil {
		resp, err = s.c.Do(ctx, method, path, reader)
	} else {
		resp, err = s.c.Do(ctx, method, path, nil)
	}
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		e := resp.Error()
		msg := e.Code + ": " + e.Message
		for _, p := range e.Problems {
			msg += fmt.Sprintf("; %s %s", p.Field, p.Message)
		}
		return nil, errors.New(msg)
	}
	if len(bytes.TrimSpace(resp.Body)) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		return map[string]any{"text": string(resp.Body)}, nil
	}
	return v, nil
}

// Tool inputs.
type (
	empty    struct{}
	changeID struct {
		ChangeID string `json:"change_id" jsonschema:"the change id, such as c_abc123"`
	}
	criterion struct {
		Text  string `json:"text" jsonschema:"what must hold, in one sentence"`
		Check *struct {
			Steps   []string `json:"steps,omitempty" jsonschema:"pipeline steps that must succeed"`
			Command string   `json:"command,omitempty" jsonschema:"a command the deterministic validator runs"`
		} `json:"check,omitempty" jsonschema:"how the deterministic validator proves it; without one the criterion is unknown until a person looks"`
	}
	submitIn struct {
		RepoID             string      `json:"repo_id" jsonschema:"the repository id from list_repos"`
		SourceBranch       string      `json:"source_branch" jsonschema:"the branch holding the change, already pushed"`
		TargetBranch       string      `json:"target_branch,omitempty" jsonschema:"defaults to main"`
		Title              string      `json:"title"`
		Intent             string      `json:"intent" jsonschema:"why, in a few sentences; the validators check the diff against it"`
		AcceptanceCriteria []criterion `json:"acceptance_criteria"`
		Constraints        []string    `json:"constraints,omitempty"`
		SubmittedBy        string      `json:"submitted_by,omitempty" jsonschema:"ignored: the session identity is used"`
	}
	registerRepoIn struct {
		Host              string   `json:"host" jsonschema:"github, azuredevops or git (a plain git URL or path)"`
		URL               string   `json:"url" jsonschema:"the clone URL; a private repository also needs a token, which a person sets in Settings"`
		ProtectedBranches []string `json:"protected_branches,omitempty" jsonschema:"branches only Plumbix merges into; default main"`
		MergeStrategy     string   `json:"merge_strategy,omitempty" jsonschema:"squash (default), merge or rebase"`
	}
	listChangesIn struct {
		RepoID string `json:"repo_id,omitempty"`
		State  string `json:"state,omitempty" jsonschema:"open, running, approved, fix_requested, rejected, needs_human, escalated, merged or closed"`
	}
	stepLogIn struct {
		RunID string `json:"run_id"`
		Step  string `json:"step"`
	}
	waitIn struct {
		ChangeID       string `json:"change_id"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait; default 240, at most 3300; call again after a timeout"`
	}
	approveIn struct {
		ChangeID string `json:"change_id"`
		Reason   string `json:"reason" jsonschema:"why the change is fine despite the verdict; kept as the audit trail"`
		By       string `json:"by,omitempty" jsonschema:"who approves, as Plumbix should record it; the session identity when left out"`
	}
	nextReviewIn struct {
		RepoID      string `json:"repo_id,omitempty" jsonschema:"only this repository's reviews"`
		WaitSeconds *int   `json:"wait_seconds,omitempty" jsonschema:"long-poll this many seconds for a review; default 50, at most 55"`
	}
	postVerdictIn struct {
		ReviewID string         `json:"review_id"`
		Token    string         `json:"token" jsonschema:"the token that came with the brief"`
		Verdict  map[string]any `json:"verdict" jsonschema:"verdict.json as the brief's schema describes it"`
	}
	alertsIn struct {
		IncludeResolved bool `json:"include_resolved,omitempty"`
	}
	alertID struct {
		AlertID string `json:"alert_id"`
	}
)

func (s *Server) register() {
	srv := s.Server
	changePath := func(suffix string) func(changeID) string {
		return func(in changeID) string { return "/api/v1/changes/" + url.PathEscape(in.ChangeID) + suffix }
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_repos", Description: "The repositories registered with Plumbix."}, simpleFor(s, http.MethodGet, func(empty) string { return "/api/v1/repos" }))
	mcp.AddTool(srv, &mcp.Tool{Name: "register_repo", Description: "Register a repository with Plumbix: host, clone URL, protected branches and merge strategy. No token travels here; a private repository gets its token from a person in Settings."}, s.registerRepo)
	mcp.AddTool(srv, &mcp.Tool{Name: "submit_change", Description: "Submit a pushed branch as a change with its intent and acceptance criteria; validation starts at once. Returns the change."}, s.submit)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_change", Description: "One change with its state, approval and merge record."}, simpleFor(s, http.MethodGet, changePath("")))
	mcp.AddTool(srv, &mcp.Tool{Name: "list_changes", Description: "Changes, optionally filtered by repository and state."}, simpleFor(s, http.MethodGet, func(in listChangesIn) string {
		q := url.Values{}
		if in.RepoID != "" {
			q.Set("repo_id", in.RepoID)
		}
		if in.State != "" {
			q.Set("state", in.State)
		}
		if len(q) == 0 {
			return "/api/v1/changes"
		}
		return "/api/v1/changes?" + q.Encode()
	}))
	mcp.AddTool(srv, &mcp.Tool{Name: "get_runs", Description: "A change's runs, newest last, each with its steps and the validators' combined verdict: criteria, findings with file and line, risks."}, simpleFor(s, http.MethodGet, changePath("/runs")))
	mcp.AddTool(srv, &mcp.Tool{Name: "get_step_log", Description: "The log of one step of a run."}, simpleFor(s, http.MethodGet, func(in stepLogIn) string {
		return "/api/v1/runs/" + url.PathEscape(in.RunID) + "/steps/" + url.PathEscape(in.Step) + "/log"
	}))
	mcp.AddTool(srv, &mcp.Tool{Name: "wait_for_verdict", Description: "Wait until the change's running iteration gets its verdict, or the change reaches a final state, or the timeout passes (default 4 minutes, at most 55; a client's own tool timeout may be shorter, raise MCP_TOOL_TIMEOUT for long waits). When it times out, call it again to keep waiting. Returns the verdict event and the change's state."}, s.wait)
	if s.allowApprove {
		mcp.AddTool(srv, &mcp.Tool{Name: "approve_change", Description: "Approve a needs_human or escalated change with a reason, as the person who started this session with --allow-approve would. The change becomes approved and merges as usual."}, s.approve)
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "merge_change", Description: "Merge an approved change; refused with target_moved or source_moved when the branches moved since the verdict."}, simpleFor(s, http.MethodPost, changePath("/merge")))
	mcp.AddTool(srv, &mcp.Tool{Name: "refresh_change", Description: "Bring the target branch into a branch Plumbix created and validate again; the answer to target_moved on such a branch."}, simpleFor(s, http.MethodPost, changePath("/refresh")))
	mcp.AddTool(srv, &mcp.Tool{Name: "close_change", Description: "Close a change without merging."}, simpleFor(s, http.MethodPost, changePath("/close")))
	mcp.AddTool(srv, &mcp.Tool{Name: "validate_change", Description: "Start a new iteration for a change, for example after a push to a needs_human change."}, simpleFor(s, http.MethodPost, changePath("/validate")))
	mcp.AddTool(srv, &mcp.Tool{Name: "next_review", Description: "Claim the next external review waiting for a reviewer. The brief carries the prompt to follow, the commits and a token; answer with post_verdict. Never returns this session's own changes."}, s.nextReview)
	mcp.AddTool(srv, &mcp.Tool{Name: "post_verdict", Description: "Post the verdict of a review claimed with next_review."}, s.postVerdict)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_alerts", Description: "Open alerts: commits that bypassed Plumbix, tokens about to expire, external reviews with no reviewer."}, simpleFor(s, http.MethodGet, func(in alertsIn) string {
		if in.IncludeResolved {
			return "/api/v1/alerts?include_resolved=true"
		}
		return "/api/v1/alerts"
	}))
	mcp.AddTool(srv, &mcp.Tool{Name: "resolve_alert", Description: "Resolve an alert a person has looked at."}, simpleFor(s, http.MethodPost, func(in alertID) string { return "/api/v1/alerts/" + url.PathEscape(in.AlertID) + "/resolve" }))
}

// simpleFor makes a tool that calls one endpoint with no body and returns its JSON.
func simpleFor[In any](s *Server, method string, path func(In) string) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := s.api(ctx, method, path(in), nil)
		if err != nil {
			return nil, nil, err
		}
		return nil, out, nil
	}
}

func (s *Server) registerRepo(ctx context.Context, _ *mcp.CallToolRequest, in registerRepoIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"host": in.Host, "url": in.URL}
	if len(in.ProtectedBranches) > 0 {
		body["protected_branches"] = in.ProtectedBranches
	}
	if in.MergeStrategy != "" {
		body["merge_strategy"] = in.MergeStrategy
	}
	out, err := s.api(ctx, http.MethodPost, "/api/v1/repos", body)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

func (s *Server) submit(ctx context.Context, _ *mcp.CallToolRequest, in submitIn) (*mcp.CallToolResult, any, error) {
	if in.TargetBranch == "" {
		in.TargetBranch = "main"
	}
	in.SubmittedBy = s.Identity
	out, err := s.api(ctx, http.MethodPost, "/api/v1/changes", in)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

func (s *Server) approve(ctx context.Context, _ *mcp.CallToolRequest, in approveIn) (*mcp.CallToolResult, any, error) {
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = s.Identity
	}
	out, err := s.api(ctx, http.MethodPost, "/api/v1/changes/"+url.PathEscape(in.ChangeID)+"/approve", map[string]string{"reason": in.Reason, "by": by})
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

func (s *Server) nextReview(ctx context.Context, _ *mcp.CallToolRequest, in nextReviewIn) (*mcp.CallToolResult, any, error) {
	wait := 50
	if in.WaitSeconds != nil {
		wait = min(max(*in.WaitSeconds, 0), MaxClaimWait)
	}
	q := url.Values{"reviewer": {s.Identity}, "wait": {strconv.Itoa(wait)}}
	if in.RepoID != "" {
		q.Set("repo_id", in.RepoID)
	}
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Second+15*time.Second)
	defer cancel()
	resp, err := s.c.Wait(pollCtx, http.MethodGet, "/api/v1/reviews/next?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	if resp.Status == http.StatusNoContent {
		return nil, map[string]any{"waiting": false, "note": "no review is waiting; call again to keep polling"}, nil
	}
	if !resp.OK() {
		e := resp.Error()
		return nil, nil, errors.New(e.Code + ": " + e.Message)
	}
	var brief map[string]any
	if err := json.Unmarshal(resp.Body, &brief); err != nil {
		return nil, nil, fmt.Errorf("decode brief: %w", err)
	}
	brief["waiting"] = true
	return nil, brief, nil
}

func (s *Server) postVerdict(ctx context.Context, _ *mcp.CallToolRequest, in postVerdictIn) (*mcp.CallToolResult, any, error) {
	out, err := s.api(ctx, http.MethodPost, "/api/v1/reviews/"+url.PathEscape(in.ReviewID)+"/verdict", map[string]any{"token": in.Token, "verdict": in.Verdict})
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// runningIteration returns the iteration of the change's running run, or 0.
func (s *Server) runningIteration(ctx context.Context, changeID string) (int, error) {
	runsOut, err := s.api(ctx, http.MethodGet, "/api/v1/changes/"+url.PathEscape(changeID)+"/runs", nil)
	if err != nil {
		return 0, err
	}
	running := 0
	if m, ok := runsOut.(map[string]any); ok {
		if runs, ok := m["runs"].([]any); ok {
			for _, r := range runs {
				rm, _ := r.(map[string]any)
				if rm["status"] == "running" {
					if it, ok := rm["iteration"].(float64); ok {
						running = int(it)
					}
				}
			}
		}
	}
	return running, nil
}

// wait follows the change's events until the iteration that is running
// gets its verdict, the change reaches a final state, or the timeout passes.
func (s *Server) wait(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, any, error) {
	timeout := DefaultWait
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, MaxWait)
	}
	state := func() any {
		c, err := s.api(ctx, http.MethodGet, "/api/v1/changes/"+url.PathEscape(in.ChangeID), nil)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return c
	}
	// Which iteration is running, if any; an older verdict replayed by the
	// stream is not the answer. A change that says running but shows no run
	// yet is caught between the submission and the engine's first record,
	// so the tool looks again for a few seconds before giving up.
	running, err := s.runningIteration(ctx, in.ChangeID)
	if err != nil {
		return nil, nil, err
	}
	for tries := 0; running == 0 && tries < 10; tries++ {
		c, _ := state().(map[string]any)
		if c == nil || c["state"] != "running" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if running, err = s.runningIteration(ctx, in.ChangeID); err != nil {
			return nil, nil, err
		}
	}
	if running == 0 {
		return nil, map[string]any{"waiting": false, "change": state(), "note": "no iteration is running; see get_runs for the latest verdict"}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var verdict any
	var reached string
	stop := errors.New("done")
	err = s.c.Stream(waitCtx, "/api/v1/events?change_id="+url.QueryEscape(in.ChangeID), 0, func(f client.Frame) error {
		var e struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(f.Data, &e) != nil {
			return nil
		}
		switch e.Type {
		case "verdict.issued":
			var p struct {
				Iteration int `json:"iteration"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if p.Iteration == running {
				var full any
				_ = json.Unmarshal(f.Data, &full)
				verdict = full
				return stop
			}
		case "change.state_changed":
			var p struct {
				To string `json:"to"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if p.To == "merged" || p.To == "closed" {
				reached = p.To
				return stop
			}
		case "run.finished":
			var p struct {
				Iteration int    `json:"iteration"`
				Status    string `json:"status"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if p.Iteration == running && p.Status == "errored" {
				reached = "errored"
				return stop
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, nil, err
	}
	if verdict == nil && reached == "" {
		return nil, map[string]any{"waiting": true, "timed_out": true, "iteration": running, "change": state(), "note": "no verdict within the timeout; call again to keep waiting"}, nil
	}
	out := map[string]any{"waiting": false, "iteration": running, "change": state()}
	if verdict != nil {
		out["verdict"] = verdict
	}
	if reached != "" {
		out["reached"] = reached
	}
	return nil, out, nil
}
