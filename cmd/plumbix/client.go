package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

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

// call sends one request and prints the result. It returns the exit code.
func (c *clientCmd) call(method, path string, body []byte) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: find home directory: %v\n", err)
		return 1
	}
	cfg, err := config.Load(c.over, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return 1
	}
	addr := c.server
	if addr == "" {
		addr = cfg.Server.Listen
	}
	token, err := auth.ReadToken(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return 1
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	resp, err := client.New(addr, token).Do(context.Background(), method, path, reader)
	if err != nil {
		fmt.Fprintf(c.stderr, "plumbix: %v\n", err)
		return 1
	}
	if !resp.OK() {
		e := resp.Error()
		fmt.Fprintf(c.stderr, "plumbix: %s: %s\n", e.Code, e.Message)
		for _, p := range e.Problems {
			fmt.Fprintf(c.stderr, "  %s: %s\n", p.Field, p.Message)
		}
		return 1
	}
	var out bytes.Buffer
	if json.Indent(&out, resp.Body, "", "  ") != nil {
		out.Reset()
		out.Write(resp.Body)
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
	}
	return usageError(stderr, "usage: plumbix repo add --host <host> --url <url> | plumbix repo list")
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
	}
	return usageError(stderr, "usage: plumbix change list [--repo <id>] [--state <s>] | change show <id> | change close <id>")
}
