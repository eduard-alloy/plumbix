// Command claude-code is Plumbix's containerised Claude Code validator
// (WU-035, A33). It runs as a pipeline step: it reads /plumbix/in and the
// workspace, runs `claude -p` there the way plumbix review --watch does on
// a machine (the same prompt, schema and tool policy), and writes
// /plumbix/out/verdict.json.
//
// Environment: PLUMBIX_AI_API_KEY holds a long-lived token from
// `claude setup-token` (a Claude subscription) or an Anthropic API key; a
// value starting with sk-ant-oat reaches claude as CLAUDE_CODE_OAUTH_TOKEN,
// anything else as ANTHROPIC_API_KEY. PLUMBIX_AI_MODEL (empty means
// claude's default), PLUMBIX_AI_TOOLS (read-only, the default, or full),
// PLUMBIX_AI_MAX_TURNS (default 40). PLUMBIX_CLAUDE names the executable;
// PLUMBIX_IN, PLUMBIX_OUT and PLUMBIX_WORKSPACE override the container
// paths.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/claudecode"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

const prefix = "claude code validator: "

func main() {
	os.Exit(run(os.Getenv, os.Stderr))
}

// run validates and writes the verdict. A non-zero exit means no verdict was
// written, which Plumbix treats as a validator error.
func run(getenv func(string) string, stderr io.Writer) int {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, prefix+format+"\n", args...)
		return 1
	}
	key := getenv("PLUMBIX_AI_API_KEY")
	if key == "" {
		return fail("PLUMBIX_AI_API_KEY is not set: the reviewer profile's key secret holds a token from `claude setup-token` or an Anthropic API key")
	}
	tools := or("PLUMBIX_AI_TOOLS", review.ToolsReadOnly)
	if tools != review.ToolsReadOnly && tools != review.ToolsFull {
		return fail("PLUMBIX_AI_TOOLS %q must be %s or %s", tools, review.ToolsReadOnly, review.ToolsFull)
	}
	maxTurns, err := strconv.Atoi(or("PLUMBIX_AI_MAX_TURNS", strconv.Itoa(claudecode.DefaultMaxTurns)))
	if err != nil || maxTurns <= 0 {
		return fail("PLUMBIX_AI_MAX_TURNS %q must be a positive number", getenv("PLUMBIX_AI_MAX_TURNS"))
	}
	inDir, workspace := or("PLUMBIX_IN", validation.InPath), or("PLUMBIX_WORKSPACE", validation.WorkspacePath)
	in, err := validation.ReadInput(inDir)
	if err != nil {
		return fail("%v", err)
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return fail("generate tag nonce: %v", err)
	}
	prompt, err := review.BuildPrompt(inDir, workspace, hex.EncodeToString(nonce))
	if err != nil {
		return fail("%v", err)
	}
	model := getenv("PLUMBIX_AI_MODEL")
	named := "claude's default model"
	if model != "" {
		named = model
	}
	fmt.Fprintf(stderr, prefix+"running claude with %s and %s tools, at most %d turns: %d characters of prompt\n", named, tools, maxTurns, len(prompt.System)+len(prompt.User))
	res, err := claudecode.Run(context.Background(), claudecode.Options{
		Claude: or("PLUMBIX_CLAUDE", "claude"), Dir: workspace, System: prompt.System, User: prompt.User,
		Schema: review.OutputSchema(review.CriterionIDs(in.Change.AcceptanceCriteria)), Tools: tools, Model: model, MaxTurns: maxTurns,
		// The credential reaches claude under the one variable it reads;
		// the raw PLUMBIX_AI_API_KEY is dropped from the environment claude
		// and the commands it runs inherit.
		Env:      []string{authVar(key) + "=" + key},
		Drop:     []string{"PLUMBIX_AI_API_KEY"},
		Progress: func(line string) { fmt.Fprintln(stderr, prefix+line) },
	})
	if err != nil {
		return fail("%v", err)
	}
	if model == "" {
		model = res.Model()
	}
	if model == "" {
		model = "claude-code"
	}
	v, err := review.ToVerdict(res.StructuredOutput, in.Change.AcceptanceCriteria, res.VerdictUsage(model))
	if err != nil {
		return fail("claude's answer is not a verdict Plumbix accepts: %v", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail("%v", err)
	}
	path := filepath.Join(or("PLUMBIX_OUT", validation.OutPath), validation.VerdictFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fail("write verdict: %v", err)
	}
	fmt.Fprintf(stderr, prefix+"%s · %d turns · %s · %s (%d input, %d read from cache, %d written to cache, %d output tokens): %s\n", v.Outcome, res.NumTurns, claudecode.CostText(res.TotalCostUSD), model,
		res.Usage.InputTokens, res.Usage.CacheReadInputTokens, res.Usage.CacheCreationInputTokens, res.Usage.OutputTokens, firstLine(v.Summary))
	return 0
}

// authVar names the variable claude reads a credential from: a token from
// `claude setup-token` carries the sk-ant-oat prefix, a key the API's.
func authVar(key string) string {
	if strings.HasPrefix(key, "sk-ant-oat") {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return "ANTHROPIC_API_KEY"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return claudecode.Excerpt(s)
}
