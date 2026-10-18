// Package reviewer is plumbix review --watch: it pulls review briefs from a
// Plumbix server, runs Claude Code non-interactively on a checkout of the
// change, and posts the verdict (WU-023, A26). It uses whatever
// authentication Claude Code already has on this machine.
package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Defaults for a review run.
const (
	DefaultWait     = 50 // seconds a claim waits for a brief
	DefaultMaxTurns = 40
	retryAfterError = 5 * time.Second
	// defaultProgressEvery keeps progress posts under the server's rate.
	defaultProgressEvery = 600 * time.Millisecond
	// maxProgressArg bounds what a progress line shows of a command or pattern.
	maxProgressArg = 120
	// task is the short prompt; the brief's user prompt arrives on stdin,
	// which has no size limit, where a command-line argument has.
	task = "Review the change described on standard input, following your instructions, and return the verdict in the required shape."
)

// Options configure Run.
type Options struct {
	Client   *client.Client
	Reviewer string // the name the server logs and uses for the own-change rule
	RepoID   string // only this repository's reviews, when set
	Once     bool   // return after one review instead of looping
	Tools    string // overrides the brief's tool policy when set
	Model    string // overrides the brief's model when set
	Claude   string // the claude executable; "claude" on the PATH by default
	WorkDir  string // mirrors and worktrees live under here
	MaxTurns int
	// MaxBudgetUSD caps one review's spend as Claude Code estimates it; zero sets no cap.
	MaxBudgetUSD float64
	Wait         int // long-poll seconds per claim
	Stdout       io.Writer
	Stderr       io.Writer
	// progressEvery is the least time between two progress lines posted
	// to the server; zero means the default, a negative value no limit
	// (tests).
	progressEvery time.Duration
}

// Run claims reviews and answers them until ctx ends, or after one review
// with Once. Errors answering a review are reported and, unless Once, do
// not stop the loop: the step times out on the server and a person sees it.
func Run(ctx context.Context, o Options) error {
	if o.Client == nil || o.Reviewer == "" || o.WorkDir == "" {
		return errors.New("reviewer: a client, a reviewer name and a work directory are required")
	}
	if o.Claude == "" {
		o.Claude = "claude"
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	if o.Wait == 0 {
		o.Wait = DefaultWait
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.progressEvery == 0 {
		o.progressEvery = defaultProgressEvery
	}
	for {
		b, ok, err := next(ctx, o)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(o.Stderr, "plumbix review: %v\n", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(retryAfterError):
			}
			continue
		}
		if !ok {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		err = reviewOne(ctx, o, b)
		if err != nil {
			fmt.Fprintf(o.Stderr, "plumbix review: %s: %v\n", b.ID, err)
		}
		if o.Once {
			return err
		}
	}
}

// next claims a brief, long-polling once. ok is false when nothing waits.
func next(ctx context.Context, o Options) (review.Brief, bool, error) {
	q := url.Values{"reviewer": {o.Reviewer}, "wait": {strconv.Itoa(o.Wait)}}
	if o.RepoID != "" {
		q.Set("repo_id", o.RepoID)
	}
	// The server holds the request up to wait seconds; the request timeout
	// of a plain call is shorter and would abandon a claim already made.
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(o.Wait)*time.Second+15*time.Second)
	defer cancel()
	resp, err := o.Client.Wait(pollCtx, http.MethodGet, "/api/v1/reviews/next?"+q.Encode(), nil)
	if err != nil {
		return review.Brief{}, false, err
	}
	if resp.Status == http.StatusNoContent {
		return review.Brief{}, false, nil
	}
	if !resp.OK() {
		return review.Brief{}, false, resp.Error()
	}
	var b review.Brief
	if err := json.Unmarshal(resp.Body, &b); err != nil {
		return review.Brief{}, false, fmt.Errorf("decode brief: %w", err)
	}
	return b, true, nil
}

// reviewOne checks the head out, runs claude on it, and posts the verdict.
func reviewOne(ctx context.Context, o Options, b review.Brief) error {
	wt, cleanup, err := checkout(ctx, o.WorkDir, b)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Fprintf(o.Stdout, "review %s: %s %s, iteration %d, head %s\n", b.ID, b.ChangeID, b.Step, b.Iteration, short(b.Head))

	// Progress lines go to the step log while Claude works, so the change
	// page shows what is being read and run.
	var lastPost time.Time
	progress := func(line string) {
		if o.progressEvery > 0 && time.Since(lastPost) < o.progressEvery {
			return
		}
		lastPost = time.Now()
		body, _ := json.Marshal(map[string]string{"token": b.Token, "line": line})
		if resp, err := o.Client.Do(ctx, http.MethodPost, "/api/v1/reviews/"+url.PathEscape(b.ID)+"/log", bytes.NewReader(body)); err == nil && !resp.OK() && resp.Status != http.StatusTooManyRequests {
			fmt.Fprintf(o.Stderr, "plumbix review: %s: progress line refused: %v\n", b.ID, resp.Error())
		}
	}
	res, err := runClaude(ctx, o, b, wt, progress)
	if err != nil {
		return err
	}
	criteria := make([]change.Criterion, 0, len(b.Criteria))
	for _, id := range b.Criteria {
		criteria = append(criteria, change.Criterion{ID: id})
	}
	model := b.Model
	if o.Model != "" {
		model = o.Model
	}
	if model == "" {
		model = res.model()
	}
	if model == "" {
		model = "claude-code"
	}
	v, err := review.ToVerdict(res.StructuredOutput, criteria, validation.Usage{Model: model, InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, Turns: res.NumTurns, CostUSD: res.TotalCostUSD})
	if err != nil {
		return fmt.Errorf("claude's answer is not a verdict Plumbix accepts: %w", err)
	}
	body, _ := json.Marshal(struct {
		Token   string             `json:"token"`
		Verdict validation.Verdict `json:"verdict"`
	}{b.Token, v})
	resp, err := o.Client.Do(ctx, http.MethodPost, "/api/v1/reviews/"+url.PathEscape(b.ID)+"/verdict", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post verdict: %w", err)
	}
	if !resp.OK() {
		return fmt.Errorf("post verdict: %w", resp.Error())
	}
	fmt.Fprintf(o.Stdout, "review %s: %s · %d turns · $%.2f · %s\n", b.ID, v.Outcome, res.NumTurns, res.TotalCostUSD, firstLine(v.Summary))
	return nil
}

// checkout materialises the brief's head in a worktree under workDir, with
// a bare mirror per repository that is fetched before each review. The
// user's own git access applies. cleanup removes the worktree.
func checkout(ctx context.Context, workDir string, b review.Brief) (string, func(), error) {
	mirror := filepath.Join(workDir, "repos", b.RepoID+".git")
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	if _, err := os.Stat(mirror); err != nil {
		if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
			return "", nil, err
		}
		if err := git("clone", "--quiet", "--bare", b.RepoURL, mirror); err != nil {
			return "", nil, err
		}
	}
	if err := git("-C", mirror, "fetch", "--quiet", "--prune", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		return "", nil, err
	}
	if err := git("-C", mirror, "cat-file", "-e", b.Head+"^{commit}"); err != nil {
		return "", nil, fmt.Errorf("head %s is not on %s after a fetch: %w", short(b.Head), b.RepoURL, err)
	}
	wt := filepath.Join(workDir, "worktrees", b.ID)
	if err := git("-C", mirror, "worktree", "add", "--quiet", "--detach", wt, b.Head); err != nil {
		return "", nil, err
	}
	cleanup := func() {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(bg, "git", "-C", mirror, "worktree", "remove", "--force", wt).Run()
	}
	return wt, cleanup, nil
}

// result is what claude -p --output-format json prints.
type result struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	SessionID        string          `json:"session_id"`
	Usage            struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens      int `json:"outputTokens"`
		OutputTokensSnake int `json:"output_tokens"`
	} `json:"modelUsage"`
}

// model names the model that did the review: the one with the most output
// tokens among those claude lists (helper turns may use a smaller one),
// ties broken by name so the record is stable.
func (r result) model() string {
	best, bestTokens := "", -1
	for name, u := range r.ModelUsage {
		tokens := max(u.OutputTokens, u.OutputTokensSnake)
		if tokens > bestTokens || (tokens == bestTokens && name < best) {
			best, bestTokens = name, tokens
		}
	}
	return best
}

// runClaude runs claude -p in the worktree with the brief's prompt and
// schema, under the operator's tool policy and the review's expiry. It
// reads the stream-json output as it arrives: each tool use becomes a
// progress line, the final result object is returned.
func runClaude(ctx context.Context, o Options, b review.Brief, worktree string, progress func(string)) (result, error) {
	tmp, err := os.MkdirTemp("", "plumbix-review-")
	if err != nil {
		return result{}, err
	}
	defer os.RemoveAll(tmp)
	sysFile := filepath.Join(tmp, "system-prompt.txt")
	if err := os.WriteFile(sysFile, []byte(b.Prompt.System), 0o600); err != nil {
		return result{}, err
	}
	schema, err := json.Marshal(b.Schema)
	if err != nil {
		return result{}, err
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--system-prompt-file", sysFile,
		"--no-session-persistence", "--permission-prompts", "none", "--max-turns", strconv.Itoa(o.MaxTurns)}
	tools := b.Tools
	if o.Tools != "" {
		tools = o.Tools
	}
	if tools == review.ToolsFull {
		// Read, search and run, never edit: the workspace is evidence.
		args = append(args, "--tools", "Bash,Read,Grep,Glob", "--allowedTools", "Bash,Read,Grep,Glob")
	} else {
		args = append(args, "--restricted", "--tools", "Read,Grep,Glob")
	}
	model := b.Model
	if o.Model != "" {
		model = o.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if o.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(o.MaxBudgetUSD, 'f', 2, 64))
	}
	args = append(args, task)

	runCtx := ctx
	if !b.ExpiresAt.IsZero() {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, b.ExpiresAt)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, o.Claude, args...)
	cmd.Dir = worktree
	cmd.Stdin = strings.NewReader(b.Prompt.User)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result{}, err
	}
	if err := cmd.Start(); err != nil {
		return result{}, fmt.Errorf("claude: %w", err)
	}
	res, found, raw := readStream(stdout, worktree, progress)
	runErr := cmd.Wait()
	if !found {
		if runErr != nil {
			return result{}, fmt.Errorf("claude: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return result{}, fmt.Errorf("claude printed no JSON result: %s", excerpt(raw))
	}
	if res.IsError || runErr != nil {
		msg := res.Result
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		return result{}, fmt.Errorf("claude did not finish the review (%s): %s", res.Subtype, excerpt(msg))
	}
	if len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null" {
		return result{}, fmt.Errorf("claude returned no structured output: %s", excerpt(res.Result))
	}
	return res, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return excerpt(s)
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// streamEvent is one object of claude's stream-json output.
type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// readStream decodes JSON objects from claude's stdout as they arrive. Tool
// uses in assistant messages are reported through progress with the turn
// they belong to; the object of type result is returned. A plain JSON
// result with no stream around it is accepted too. raw holds what was read
// when no result was found, for the error message.
func readStream(r io.Reader, worktree string, progress func(string)) (result, bool, string) {
	var seen bytes.Buffer
	dec := json.NewDecoder(io.TeeReader(r, &seen))
	turn := 0
	var res result
	found := false
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		var ev streamEvent
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			turn++
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" && progress != nil {
					progress(fmt.Sprintf("turn %d: %s", turn, describeTool(c.Name, c.Input, worktree)))
				}
			}
		case "result":
			if json.Unmarshal(raw, &res) == nil {
				found = true
			}
		default:
			// A bare result object has no type when a wrapper prints it alone.
			if ev.Type == "" {
				var maybe result
				if json.Unmarshal(raw, &maybe) == nil && (len(maybe.StructuredOutput) > 0 || maybe.IsError || maybe.Result != "") {
					res, found = maybe, true
				}
			}
		}
	}
	// Whatever claude prints after the result (or after a line that is not
	// JSON) is read and dropped, so the process never blocks on a full pipe
	// and cmd.Wait returns.
	_, _ = io.Copy(io.Discard, r)
	if found {
		return res, true, ""
	}
	return result{}, false, seen.String()
}

// describeTool renders a tool use for the step log: the tool and what it
// touched, paths relative to the worktree, long commands cut short.
func describeTool(name string, input json.RawMessage, worktree string) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		v, _ := in[k].(string)
		return v
	}
	rel := func(p string) string {
		for _, root := range worktreeRoots(worktree) {
			if strings.HasPrefix(p, root+"/") {
				return strings.TrimPrefix(p, root+"/")
			}
			if p == root {
				return "."
			}
		}
		return p
	}
	var arg string
	switch name {
	case "Read", "Edit", "Write":
		arg = rel(str("file_path"))
	case "Grep":
		arg = str("pattern")
		if p := str("path"); p != "" && rel(p) != "." {
			arg += " in " + rel(p)
		}
	case "Glob":
		arg = str("pattern")
	case "Bash":
		arg = str("command")
	default:
		for _, k := range []string{"path", "file_path", "pattern", "query", "command", "url"} {
			if v := str(k); v != "" {
				arg = v
				break
			}
		}
	}
	arg = strings.Join(strings.Fields(arg), " ")
	if r := []rune(arg); len(r) > maxProgressArg {
		arg = string(r[:maxProgressArg]) + "…"
	}
	if arg == "" {
		return name
	}
	return name + " " + arg
}

// worktreeRoots lists the worktree path and its resolved form, since tools
// report the path they ran in, symlinks resolved.
func worktreeRoots(worktree string) []string {
	roots := []string{worktree}
	if resolved, err := filepath.EvalSymlinks(worktree); err == nil && resolved != worktree {
		roots = append(roots, resolved)
	}
	return roots
}
