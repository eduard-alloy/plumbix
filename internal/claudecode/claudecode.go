// Package claudecode runs Claude Code non-interactively as a reviewer: one
// `claude -p` with a system prompt, a user prompt, a JSON schema for the
// verdict and a tool policy, its stream-json output read as it arrives so
// tool uses become progress lines. The review loop (plumbix review --watch)
// and the containerised validator image share it (WU-023, WU-035, A33).
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/review"
)

const (
	// DefaultMaxTurns bounds one review when the caller sets no limit.
	DefaultMaxTurns = 40
	// Task is the request claude receives as its prompt; the brief follows on stdin.
	Task = "Review the change described on standard input, following your instructions, and return the verdict in the required shape."
	// maxProgressArg bounds what a progress line shows of a command or pattern.
	maxProgressArg = 120
)

// Options configure one run.
type Options struct {
	Claude   string         // the executable; "claude" on the PATH when empty
	Dir      string         // the checkout claude works in
	System   string         // the system prompt
	User     string         // the user prompt, given on stdin
	Schema   map[string]any // the verdict's JSON schema
	Tools    string         // review.ToolsReadOnly (the default) or review.ToolsFull
	Model    string         // claude's default when empty
	MaxTurns int            // DefaultMaxTurns when zero
	// MaxBudgetUSD caps one review's spend as Claude Code estimates it; zero sets no cap.
	MaxBudgetUSD float64
	// Deadline ends the run; zero means the context alone bounds it.
	Deadline time.Time
	// Env is added to the inherited environment (authentication, typically).
	Env []string
	// Progress receives one line per tool use, with the turn it belongs to.
	Progress func(string)
}

// Result is what claude -p prints as its result object.
type Result struct {
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

// Model names the model that did the review: the one with the most output
// tokens among those claude lists (helper turns may use a smaller one),
// ties broken by name so the record is stable.
func (r Result) Model() string {
	best, bestTokens := "", -1
	for name, u := range r.ModelUsage {
		tokens := max(u.OutputTokens, u.OutputTokensSnake)
		if tokens > bestTokens || (tokens == bestTokens && name < best) {
			best, bestTokens = name, tokens
		}
	}
	return best
}

// Run runs claude -p in o.Dir with the prompt and schema, under the tool
// policy and the deadline. It reads the stream-json output as it arrives:
// each tool use becomes a progress line, the final result object is
// returned. An error result, or one without structured output, is an error.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.Claude == "" {
		o.Claude = "claude"
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	tmp, err := os.MkdirTemp("", "plumbix-review-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	sysFile := filepath.Join(tmp, "system-prompt.txt")
	if err := os.WriteFile(sysFile, []byte(o.System), 0o600); err != nil {
		return Result{}, err
	}
	schema, err := json.Marshal(o.Schema)
	if err != nil {
		return Result{}, err
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--system-prompt-file", sysFile,
		"--no-session-persistence", "--permission-prompts", "none", "--max-turns", strconv.Itoa(o.MaxTurns)}
	if o.Tools == review.ToolsFull {
		// Read, search and run, never edit: the workspace is evidence.
		args = append(args, "--tools", "Bash,Read,Grep,Glob", "--allowedTools", "Bash,Read,Grep,Glob")
	} else {
		args = append(args, "--restricted", "--tools", "Read,Grep,Glob")
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(o.MaxBudgetUSD, 'f', 2, 64))
	}
	args = append(args, Task)

	runCtx := ctx
	if !o.Deadline.IsZero() {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, o.Deadline)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, o.Claude, args...)
	cmd.Dir = o.Dir
	cmd.Stdin = strings.NewReader(o.User)
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("claude: %w", err)
	}
	res, found, raw := readStream(stdout, o.Dir, o.Progress)
	runErr := cmd.Wait()
	if !found {
		if runErr != nil {
			return Result{}, fmt.Errorf("claude: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return Result{}, fmt.Errorf("claude printed no JSON result: %s", Excerpt(raw))
	}
	if res.IsError || runErr != nil {
		msg := res.Result
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		return Result{}, fmt.Errorf("claude did not finish the review (%s): %s", res.Subtype, Excerpt(msg))
	}
	if len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null" {
		return Result{}, fmt.Errorf("claude returned no structured output: %s", Excerpt(res.Result))
	}
	return res, nil
}

// Excerpt trims text to what an error message or a log line can carry.
func Excerpt(s string) string {
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
func readStream(r io.Reader, dir string, progress func(string)) (Result, bool, string) {
	var seen bytes.Buffer
	dec := json.NewDecoder(io.TeeReader(r, &seen))
	turn := 0
	var res Result
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
					progress(fmt.Sprintf("turn %d: %s", turn, describeTool(c.Name, c.Input, dir)))
				}
			}
		case "result":
			if json.Unmarshal(raw, &res) == nil {
				found = true
			}
		default:
			// A bare result object has no type when a wrapper prints it alone.
			if ev.Type == "" {
				var maybe Result
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
	return Result{}, false, seen.String()
}

// describeTool renders a tool use for the step log: the tool and what it
// touched, paths relative to the directory, long commands cut short.
func describeTool(name string, input json.RawMessage, dir string) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		v, _ := in[k].(string)
		return v
	}
	rel := func(p string) string {
		for _, root := range dirRoots(dir) {
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

// dirRoots lists the directory and its resolved form, since tools report
// the path they ran in, symlinks resolved.
func dirRoots(dir string) []string {
	roots := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		roots = append(roots, resolved)
	}
	return roots
}
