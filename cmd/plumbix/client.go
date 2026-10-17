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
	"strings"

	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/config"
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
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: find home directory: %v\n", err)
		return nil, false
	}
	cfg, err := config.Load(c.over, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return nil, false
	}
	addr := c.server
	if addr == "" {
		addr = cfg.Server.Listen
	}
	token, err := auth.ReadToken(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return nil, false
	}
	return client.New(addr, token), true
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

// runRepo handles "plumbix repo add|list".
func runRepo(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("repo", stdout, stderr)
	var in struct {
		Host              string     `json:"host"`
		URL               string     `json:"url"`
		ProtectedBranches stringList `json:"protected_branches,omitempty"`
		MergeStrategy     string     `json:"merge_strategy,omitempty"`
	}
	c.fs.StringVar(&in.Host, "host", "", "github or azuredevops (repo add)")
	c.fs.StringVar(&in.URL, "url", "", "HTTPS clone URL (repo add)")
	c.fs.Var(&in.ProtectedBranches, "protected", "protected branch, repeatable (repo add, default main)")
	c.fs.StringVar(&in.MergeStrategy, "merge-strategy", "", "squash, merge or rebase (repo add, default squash)")
	branch := c.fs.String("branch", "", "only runs of this branch (repo runs)")
	pos, ok := c.parse(args)
	if !ok {
		return 2
	}
	switch {
	case len(pos) == 1 && pos[0] == "add":
		body, _ := json.Marshal(in)
		return c.call(http.MethodPost, "/api/v1/repos", body)
	case len(pos) == 1 && pos[0] == "list":
		return c.call(http.MethodGet, "/api/v1/repos", nil)
	case len(pos) == 2 && pos[0] == "runs":
		path := "/api/v1/repos/" + url.PathEscape(pos[1]) + "/runs"
		if *branch != "" {
			path += "?branch=" + url.QueryEscape(*branch)
		}
		return c.call(http.MethodGet, path, nil)
	}
	return usageError(stderr, "usage: plumbix repo add --host <host> --url <url> | repo list | repo runs <id> [--branch <b>]")
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

// runChange handles "plumbix change list|show|close".
func runChange(args []string, stdout, stderr io.Writer) int {
	c := newClientCmd("change", stdout, stderr)
	repo := c.fs.String("repo", "", "filter by repo id (change list)")
	state := c.fs.String("state", "", "filter by state (change list)")
	reason := c.fs.String("reason", "", "why the change is fine despite the verdict (change approve)")
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
		body, _ := json.Marshal(map[string]string{"reason": *reason})
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/approve", body)
	case len(pos) == 2 && pos[0] == "validate":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/validate", nil)
	case len(pos) == 2 && pos[0] == "merge":
		return c.call(http.MethodPost, "/api/v1/changes/"+url.PathEscape(pos[1])+"/merge", nil)
	case len(pos) == 2 && pos[0] == "runs":
		return c.call(http.MethodGet, "/api/v1/changes/"+url.PathEscape(pos[1])+"/runs", nil)
	}
	return usageError(stderr, "usage: plumbix change list [--repo <id>] [--state <s>] | change show|close|validate|merge|runs <id> | change approve <id> --reason <why>")
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
