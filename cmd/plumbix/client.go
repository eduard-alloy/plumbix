package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/config"
	"github.com/eduard-alloy/plumbix/internal/mcpserver"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/reviewer"
)

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return fmt.Sprint(*s) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// clientCmd holds the flags every client command shares.
type clientCmd struct {
	fs     *flag.FlagSet
	over   config.Overrides
	server string
	stdout io.Writer
	stderr io.Writer
}

func newClientCmd(name string, stdout, stderr io.Writer) *clientCmd {
	c := &clientCmd{fs: flag.NewFlagSet(name, flag.ContinueOnError), stdout: stdout, stderr: stderr}
	c.fs.SetOutput(stderr)
	c.fs.StringVar(&c.over.ConfigPath, "config", "", "config file (default <data-dir>/config.yaml)")
	c.fs.StringVar(&c.over.DataDir, "data-dir", "", "data directory holding the API token (default ~/.plumbix)")
	c.fs.StringVar(&c.server, "server", "", "server address host:port (default: server.listen from config)")
	return c
}

// parse accepts flags before, between and after positional arguments.
func (c *clientCmd) parse(args []string) ([]string, bool) {
	var positional []string
	for {
		if err := c.fs.Parse(args); err != nil {
			return nil, false
		}
		if c.fs.NArg() == 0 {
			return positional, true
		}
		positional = append(positional, c.fs.Arg(0))
		args = c.fs.Args()[1:]
	}
}

// connect loads config and the token and returns a client, or prints the
// problem and returns false.
func (c *clientCmd) connect() (*client.Client, bool) {
	cl, _, ok := c.connectWithConfig()
	return cl, ok
}

// connectWithConfig is connect plus the configuration it read, for commands
// that also need the data directory.
func (c *clientCmd) connectWithConfig() (*client.Client, config.Config, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: find home directory: %v\n", err)
		return nil, config.Config{}, false
	}
	cfg, err := config.Load(c.over, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return nil, config.Config{}, false
	}
	addr := c.server
	if addr == "" {
		addr = cfg.Server.Listen
	}
	token, err := auth.ReadToken(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return nil, config.Config{}, false
	}
	return client.New(addr, token), cfg, true
}

// printAPIError prints an error response the way every command does.
func (c *clientCmd) printAPIError(e client.APIError) {
	fmt.Fprintf(c.stderr, "plumbix: %s: %s\n", e.Code, e.Message)
	for _, p := range e.Problems {
		fmt.Fprintf(c.stderr, "  %s: %s\n", p.Field, p.Message)
	}
}

// call sends one request and prints the result. It returns the exit code.
func (c *clientCmd) call(method, path string, body []byte) int {
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	resp, err := cl.Do(context.Background(), method, path, reader)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return 1
	}
	if !resp.OK() {
		c.printAPIError(resp.Error())
		return 1
	}
	var out bytes.Buffer
	if json.Indent(&out, resp.Body, "", "  ") != nil {
		// Not JSON (a log, for example): print it unchanged.
		_, _ = c.stdout.Write(resp.Body)
		return 0
	}
	fmt.Fprintln(c.stdout, out.String())
	return 0
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "plumbix: %s\n", msg)
	return 2
}

// runRepo handles "plumbix repo add|set|list|runs".
func runRepo(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("repo", stdout, stderr)
	var in struct {
		Host              string     `json:"host"`
		URL               string     `json:"url"`
		ProtectedBranches stringList `json:"protected_branches,omitempty"`
		MergeStrategy     string     `json:"merge_strategy,omitempty"`
		PullRequests      bool       `json:"pull_requests,omitempty"`
		Merger            string     `json:"merger,omitempty"`
	}
	c.fs.StringVar(&in.Host, "host", "", "github or azuredevops (repo add)")
	c.fs.StringVar(&in.URL, "url", "", "HTTPS clone URL (repo add)")
	c.fs.Var(&in.ProtectedBranches, "protected", "protected branch, repeatable (repo add, default main)")
	c.fs.StringVar(&in.MergeStrategy, "merge-strategy", "", "squash, merge or rebase (repo add, default squash)")
	c.fs.BoolVar(&in.PullRequests, "pull-requests", false, "gate the repository's GitHub pull requests with check runs (repo add, repo set)")
	c.fs.StringVar(&in.Merger, "merger", "", "who lands changes: plumbix or github (repo add, repo set; default github with --pull-requests, else plumbix)")
	branch := c.fs.String("branch", "", "only runs of this branch (repo runs)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 1 && pos[0] == "add":
		body, _ := json.Marshal(in)
		return c.call(http.MethodPost, "/api/v1/repos", body)
	case len(pos) == 2 && pos[0] == "set":
		// Only the flags given travel; the server leaves the rest as it is.
		set := map[string]any{}
		c.fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "pull-requests":
				set["pull_requests"] = in.PullRequests
			case "merger":
				set["merger"] = in.Merger
			}
		})
		if len(set) == 0 {
			return usageError(stderr, "usage: plumbix repo set <id> [--pull-requests[=false]] [--merger plumbix|github]")
		}
		body, _ := json.Marshal(set)
		return c.call(http.MethodPut, "/api/v1/repos/"+url.PathEscape(pos[1])+"/settings", body)
	case len(pos) == 1 && pos[0] == "list":
		return c.call(http.MethodGet, "/api/v1/repos", nil)
	case len(pos) == 2 && pos[0] == "github":
		return c.call(http.MethodGet, "/api/v1/repos/"+url.PathEscape(pos[1])+"/github", nil)
	case len(pos) == 2 && pos[0] == "runs":
		path := "/api/v1/repos/" + url.PathEscape(pos[1]) + "/runs"
		if *branch != "" {
			path += "?branch=" + url.QueryEscape(*branch)
		}
		return c.call(http.MethodGet, path, nil)
	}
	return usageError(stderr, "usage: plumbix repo add --host <host> --url <url> [--pull-requests] [--merger plumbix|github] | repo set <id> [--pull-requests[=false]] [--merger plumbix|github] | repo list | repo github <id> | repo runs <id> [--branch <b>]")
}

// runSubmit handles "plumbix submit -f <file|->".
func runSubmit(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newClientCmd("submit", stdout, stderr)
	file := c.fs.String("f", "", "change JSON file, or - for standard input")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if *file == "" || len(pos) != 0 {
		return usageError(stderr, "usage: plumbix submit -f <change.json | ->")
	}
	var body []byte
	var err error
	if *file == "-" {
		if stdin == nil {
			stdin = os.Stdin
		}
		body, err = io.ReadAll(stdin)
	} else {
		body, err = os.ReadFile(*file)
	}
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: read change: %v\n", err)
		return 1
	}
	return c.call(http.MethodPost, "/api/v1/changes", body)
}

// runChange handles "plumbix change list|show|close|validate|refresh|merge|runs|approve".
func runChange(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("change", stdout, stderr)
	repo := c.fs.String("repo", "", "filter by repo id (change list)")
	state := c.fs.String("state", "", "filter by state (change list)")
	reason := c.fs.String("reason", "", "why the change is fine despite the verdict (change approve)")
	by := c.fs.String("by", defaultApprover(), "who approves, as Plumbix should record it (change approve)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 1 && pos[0] == "list":
		q := url.Values{}
		if *repo != "" {
			q.Set("repo_id", *repo)
		}
		if *state != "" {
			q.Set("state", *state)
		}
		path := "/api/v1/changes"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		return c.call(http.MethodGet, path, nil)
	case len(pos) == 2 && pos[0] == "show":
		return c.call(http.MethodGet, "/api/v1/changes/"+url.PathEscape(pos[1]), nil)
	case len(pos) == 2 && pos[0] == "close":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/close", nil)
	case len(pos) == 2 && pos[0] == "approve":
		if strings.TrimSpace(*reason) == "" {
			return usageError(stderr, "plumbix change approve needs --reason: say why the change is fine despite the verdict")
		}
		if strings.TrimSpace(*by) == "" {
			return usageError(stderr, "plumbix change approve needs --by: name the person approving")
		}
		body, _ := json.Marshal(map[string]string{"reason": *reason, "by": strings.TrimSpace(*by)})
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/approve", body)
	case len(pos) == 2 && pos[0] == "validate":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/validate", nil)
	case len(pos) == 2 && pos[0] == "refresh":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/refresh", nil)
	case len(pos) == 2 && pos[0] == "merge":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/merge", nil)
	case len(pos) == 2 && pos[0] == "runs":
		return c.call(http.MethodGet, "/api/v1/changes/"+url.PathEscape(pos[1])+"/runs", nil)
	}
	return usageError(stderr, "usage: plumbix change list [--repo <id>] [--state <s>] | change show|close|validate|refresh|merge|runs <id> | change approve <id> --reason <why>")
}

// runPipeline handles "plumbix pipeline list|run" (WU-045): the triggered
// pipelines under .plumbix/pipelines/.
func runPipeline(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("pipeline", stdout, stderr)
	branch := c.fs.String("branch", "", "branch head to read or run at (default: the first protected branch)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 2 && pos[0] == "list":
		path := "/api/v1/repos/" + url.PathEscape(pos[1]) + "/pipelines"
		if *branch != "" {
			path += "?branch=" + url.QueryEscape(*branch)
		}
		return c.call(http.MethodGet, path, nil)
	case len(pos) == 3 && pos[0] == "run":
		body, _ := json.Marshal(map[string]string{"branch": *branch})
		return c.call(http.MethodPost, "/api/v1/repos/"+url.PathEscape(pos[1])+"/pipelines/"+url.PathEscape(pos[2])+"/runs", body)
	}
	return usageError(stderr, "usage: plumbix pipeline list <repo-id> [--branch <b>] | plumbix pipeline run <repo-id> <name> [--branch <b>]")
}

// runImport handles "plumbix import preview|submit" (WU-046, WU-048): what
// importing a repository's CI pipelines would write, and the change that
// writes it.
func runImport(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("import", stdout, stderr)
	branch := c.fs.String("branch", "", "preview: branch head to read (default: the first protected branch)")
	target := c.fs.String("target", "", "submit: branch to read and build on (default: the first protected branch)")
	newBranch := c.fs.String("new-branch", "", "submit: the branch to push (default: plumbix/import-<head>)")
	title := c.fs.String("title", "", "submit: the change title")
	source := c.fs.String("source", "azure", "the CI system to import from")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 2 && pos[0] == "preview":
		q := url.Values{"source": {*source}}
		if *branch != "" {
			q.Set("branch", *branch)
		}
		return c.call(http.MethodGet, "/api/v1/repos/"+url.PathEscape(pos[1])+"/import?"+q.Encode(), nil)
	case len(pos) == 2 && pos[0] == "submit":
		body, _ := json.Marshal(map[string]string{"source": *source, "target_branch": *target, "branch": *newBranch, "title": *title})
		return c.call(http.MethodPost, "/api/v1/repos/"+url.PathEscape(pos[1])+"/import/changes", body)
	}
	return usageError(stderr, "usage: plumbix import preview <repo-id> [--branch <b>] [--source azure] | plumbix import submit <repo-id> [--target <b>] [--new-branch <b>] [--title <t>] [--source azure]")
}

// runAlert handles "plumbix alert list|resolve".
func runAlert(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("alert", stdout, stderr)
	all := c.fs.Bool("all", false, "include resolved alerts (alert list)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 1 && pos[0] == "list":
		path := "/api/v1/alerts"
		if *all {
			path += "?include_resolved=true"
		}
		return c.call(http.MethodGet, path, nil)
	case len(pos) == 2 && pos[0] == "resolve":
		return c.call(http.MethodPost, "/api/v1/alerts/"+url.PathEscape(pos[1])+"/resolve", nil)
	}
	return usageError(stderr, "usage: plumbix alert list [--all] | plumbix alert resolve <alert-id>")
}

// runLogin handles "plumbix login": it prints a one-time link that signs
// a browser in to this server.
func runLogin(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("login", stdout, stderr)
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if len(pos) != 0 {
		return usageError(stderr, "usage: plumbix login")
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	resp, err := cl.Do(context.Background(), http.MethodPost, "/api/v1/login-links", nil)
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
		return 1
	}
	if !resp.OK() {
		c.printAPIError(resp.Error())
		return 1
	}
	var link struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(resp.Body, &link); err != nil || link.URL == "" {
		fmt.Fprintf(stderr, "plumbix: unexpected response: %s\n", resp.Body)
		return 1
	}
	fmt.Fprintf(stdout, "Open this link in your browser to sign in. It works once and expires in 10 minutes.\n\n  %s\n\n", link.URL)
	return 0
}

// runLogout handles "plumbix logout": every browser session is revoked.
func runLogout(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("logout", stdout, stderr)
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if len(pos) != 0 {
		return usageError(stderr, "usage: plumbix logout")
	}
	return c.call(http.MethodDelete, "/api/v1/sessions", nil)
}

// errEnoughEvents stops the stream once --max events were printed.
var errEnoughEvents = errors.New("enough events")

// runEvents handles "plumbix events": one JSON event per line, from the
// first recorded event or after --after, until interrupted or --max.
func runEvents(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("events", stdout, stderr)
	repo := c.fs.String("repo", "", "only events of this repo")
	changeID := c.fs.String("change", "", "only events of this change")
	after := c.fs.Int64("after", 0, "resume after this event id")
	limit := c.fs.Int("max", 0, "stop after this many events (default: until interrupted)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if len(pos) != 0 || *after < 0 || *limit < 0 {
		return usageError(stderr, "usage: plumbix events [--repo <id>] [--change <id>] [--after <event-id>] [--max <n>]")
	}
	q := url.Values{}
	if *repo != "" {
		q.Set("repo_id", *repo)
	}
	if *changeID != "" {
		q.Set("change_id", *changeID)
	}
	path := "/api/v1/events"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	printed := 0
	err := cl.Stream(ctx, path, *after, func(f client.Frame) error {
		fmt.Fprintf(stdout, "%s\n", f.Data)
		printed++
		if *limit > 0 && printed >= *limit {
			return errEnoughEvents
		}
		return nil
	})
	var apiErr client.APIError
	switch {
	case err == nil || errors.Is(err, errEnoughEvents):
		return 0
	case errors.As(err, &apiErr):
		c.printAPIError(apiErr)
	default:
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
	}
	return 1
}

// runRun handles "plumbix run show|log".
func runRun(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("run", stdout, stderr)
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 2 && pos[0] == "show":
		return c.call(http.MethodGet, "/api/v1/runs/"+url.PathEscape(pos[1]), nil)
	case len(pos) == 3 && pos[0] == "log":
		return c.call(http.MethodGet, "/api/v1/runs/"+url.PathEscape(pos[1])+"/steps/"+url.PathEscape(pos[2])+"/log", nil)
	}
	return usageError(stderr, "usage: plumbix run show <run-id> | plumbix run log <run-id> <step>")
}

// runReview handles "plumbix review --watch|--once": it pulls review briefs
// from the server and answers them with Claude Code (WU-023).
func runReview(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("review", stdout, stderr)
	watch := c.fs.Bool("watch", false, "keep reviewing until interrupted")
	once := c.fs.Bool("once", false, "answer one review, then exit")
	repo := c.fs.String("repo", "", "only this repository's reviews")
	who := c.fs.String("reviewer", "", "the name the server logs for this reviewer (default: user@host)")
	tools := c.fs.String("tools", "", "read-only or full; overrides the server's setting for this reviewer")
	model := c.fs.String("model", "", "model for claude; overrides the server's setting")
	claudeBin := c.fs.String("claude", "claude", "the claude executable")
	maxTurns := c.fs.Int("max-turns", reviewer.DefaultMaxTurns, "most agentic turns per review")
	budget := c.fs.Float64("max-budget-usd", 0, "most to spend per review as Claude Code estimates it; 0 for no cap")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if *tools != "" && *tools != review.ToolsReadOnly && *tools != review.ToolsFull {
		return usageError(stderr, "plumbix review: --tools must be read-only or full")
	}
	const use = "usage: plumbix review --watch|--once [--repo <id>] [--reviewer <name>] [--tools read-only|full] [--model <m>] [--max-turns <n>] [--max-budget-usd <x>] | review install [same flags] | review uninstall | review status"
	if len(pos) == 1 && (pos[0] == "install" || pos[0] == "uninstall" || pos[0] == "status") {
		if *watch || *once {
			return usageError(stderr, use)
		}
		// The installed loop gets the flags given here, plus the server and
		// data-dir overrides, so it reviews the way this command would.
		var loopArgs []string
		if *who != "" {
			loopArgs = append(loopArgs, "--reviewer", *who)
		}
		if *repo != "" {
			loopArgs = append(loopArgs, "--repo", *repo)
		}
		if *tools != "" {
			loopArgs = append(loopArgs, "--tools", *tools)
		}
		if *model != "" {
			loopArgs = append(loopArgs, "--model", *model)
		}
		if *claudeBin != "claude" {
			loopArgs = append(loopArgs, "--claude", *claudeBin)
		}
		if *maxTurns != reviewer.DefaultMaxTurns {
			loopArgs = append(loopArgs, "--max-turns", strconv.Itoa(*maxTurns))
		}
		if *budget > 0 {
			loopArgs = append(loopArgs, "--max-budget-usd", strconv.FormatFloat(*budget, 'f', 2, 64))
		}
		if c.server != "" {
			loopArgs = append(loopArgs, "--server", c.server)
		}
		if c.over.DataDir != "" {
			loopArgs = append(loopArgs, "--data-dir", c.over.DataDir)
		}
		if c.over.ConfigPath != "" {
			loopArgs = append(loopArgs, "--config", c.over.ConfigPath)
		}
		return runReviewService(pos[0], c, loopArgs, stdout, stderr)
	}
	if len(pos) != 0 || *watch == *once {
		return usageError(stderr, use)
	}
	cl, cfg, ok := c.connectWithConfig()
	if !ok {
		return 1
	}
	name := *who
	if name == "" {
		name = defaultReviewerName()
	}
	err := reviewer.Run(ctx, reviewer.Options{
		Client: cl, Reviewer: name, RepoID: *repo, Once: *once, Tools: *tools, Model: *model, Claude: *claudeBin,
		WorkDir: filepath.Join(cfg.DataDir, "review"), MaxTurns: *maxTurns, MaxBudgetUSD: *budget, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return 1
	}
	return 0
}

// defaultReviewerName is user@host, enough to tell reviewers apart in a log.
func defaultReviewerName() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "reviewer"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return user
	}
	return user + "@" + host
}

// runReviewService handles "plumbix review install|uninstall|status": an OS
// service keeps the loop alive (launchd on macOS, a systemd user unit on
// Linux); the server never spawns it (A27).
func runReviewService(verb string, c *clientCmd, loopArgs []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: find home directory: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: find this executable: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cfg, err := config.Load(c.over, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
		return 1
	}
	o := reviewer.InstallOptions{Home: home, GOOS: runtime.GOOS, UID: os.Getuid(), Exec: exe, Args: loopArgs,
		LogPath: filepath.Join(cfg.DataDir, "review.log"), Path: os.Getenv("PATH"), Run: runCommand}
	switch verb {
	case "install":
		path, err := reviewer.Install(o)
		if err != nil {
			fmt.Fprintf(stderr, "plumbix: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "installed %s\nThe reviewer loop starts at login and restarts if it stops; its log is %s.\n", path, o.LogPath)
		return 0
	case "uninstall":
		if err := reviewer.Uninstall(o); err != nil {
			fmt.Fprintf(stderr, "plumbix: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "uninstalled; the loop is stopped")
		return 0
	default:
		st, err := reviewer.Status(o)
		if err != nil {
			fmt.Fprintf(stderr, "plumbix: %v\n", err)
			return 1
		}
		switch {
		case st.Running:
			fmt.Fprintf(stdout, "installed at %s and running\n", st.Path)
		case st.Installed:
			fmt.Fprintf(stdout, "installed at %s but not running\n", st.Path)
		default:
			fmt.Fprintln(stdout, "not installed; plumbix review install keeps a loop running")
		}
		if cl, ok := c.connect(); ok {
			if resp, err := cl.Do(context.Background(), http.MethodGet, "/api/v1/reviewers", nil); err == nil && resp.OK() {
				var out bytes.Buffer
				if json.Indent(&out, resp.Body, "", "  ") == nil {
					fmt.Fprintf(stdout, "reviewers the server has heard from:\n%s\n", out.String())
				}
			}
		}
		return 0
	}
}

// runCommand runs a service-manager command and returns its combined output.
func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// runMCP handles "plumbix mcp": an MCP server over stdio for agent sessions
// (WU-024). It speaks to the Plumbix API like every client command; stdout
// is the protocol, so nothing else is printed there.
func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("mcp", stdout, stderr)
	name := c.fs.String("name", "agent", "the agent's name in this session's identity (mcp:<name>:<id>)")
	allowApprove := c.fs.Bool("allow-approve", false, "offer approve_change: only for a session a person drives, since approval is a person's act")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	if len(pos) != 0 {
		return usageError(stderr, "usage: plumbix mcp [--name <agent>] [--allow-approve]; add it to Claude Code with: claude mcp add plumbix -- plumbix mcp")
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	s := mcpserver.New(mcpserver.Options{Client: cl, Name: *name, Version: version, AllowApprove: *allowApprove})
	fmt.Fprintf(stderr, "plumbix mcp: serving as %s\n", s.Identity)
	if err := s.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "plumbix mcp: %v\n", err)
		return 1
	}
	return 0
}

// defaultApprover is user@host, the name the review loop uses for its
// reviewer too; "" when neither is known.
func defaultApprover() string {
	user := os.Getenv("USER")
	if user == "" {
		if u, err := osuser.Current(); err == nil {
			user = u.Username
		}
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	switch {
	case user == "" && host == "":
		return ""
	case user == "":
		return host
	case host == "":
		return user
	}
	return user + "@" + host
}

// runCriteria handles "plumbix criteria add|update|list|show" (WU-066):
// acceptance criteria written with the specification, which a change or
// a pull request names instead of stating its own.
func runCriteria(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newClientCmd("criteria", stdout, stderr)
	file := c.fs.String("f", "", "criteria set JSON file, or - for standard input (criteria add|update)")
	task := c.fs.String("task", "", "list the sets of one task reference (criteria list)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	readBody := func() ([]byte, bool) {
		if *file == "" {
			_ = usageError(stderr, "usage: plumbix criteria add -f <set.json | ->")
			return nil, false
		}
		var body []byte
		var err error
		if *file == "-" {
			if stdin == nil {
				stdin = os.Stdin
			}
			body, err = io.ReadAll(stdin)
		} else {
			body, err = os.ReadFile(*file)
		}
		if err != nil {
			fmt.Fprintf(stderr, "plumbix: read criteria set: %v\n", err)
			return nil, false
		}
		return body, true
	}
	switch {
	case len(pos) == 1 && pos[0] == "add":
		body, ok := readBody()
		if !ok {
			return 2
		}
		return c.call(http.MethodPost, "/api/v1/criteria-sets", body)
	case len(pos) == 2 && pos[0] == "update":
		body, ok := readBody()
		if !ok {
			return 2
		}
		return c.call(http.MethodPut, "/api/v1/criteria-sets/"+url.PathEscape(pos[1]), body)
	case len(pos) == 1 && pos[0] == "list":
		path := "/api/v1/criteria-sets"
		if *task != "" {
			path += "?task=" + url.QueryEscape(*task)
		}
		return c.call(http.MethodGet, path, nil)
	case len(pos) == 2 && pos[0] == "show":
		return c.call(http.MethodGet, "/api/v1/criteria-sets/"+url.PathEscape(pos[1]), nil)
	}
	return usageError(stderr, "usage: plumbix criteria add -f <set.json | -> | criteria update <id> -f <set.json | -> | criteria list [--task <ref>] | criteria show <id>")
}
