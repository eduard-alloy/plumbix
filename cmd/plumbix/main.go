// Command plumbix is the Plumbix server and command-line client.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: plumbix <command> [flags]

commands:
  serve                 start the Plumbix server (local addresses only)
  repo add|set|list|github|runs
                        register a repo, set its pull-request gating, list repos,
                        read a repo's GitHub integration or its runs
  submit -f <file|->    submit a change (JSON)
  change list|show|close|validate|merge|runs
                        read, close, revalidate or merge changes
  run show|log          read a run, or a step's log
  pipeline list|run     list a repo's triggered pipelines, or run one by hand
  import preview|submit show what importing a repo's Azure pipelines would write,
                        or open the change that writes it
  alert list|resolve    bypass commits and token warnings
  events                follow the event stream as JSON lines
  review --watch|--once answer external reviews with Claude Code on this machine
  review install|uninstall|status
                        keep the reviewer loop running as a login service
  mcp [--name <agent>] [--allow-approve]
                        serve Plumbix's tools to an agent session over stdio (MCP)
  login                 print a one-time link that signs a browser in
  logout                sign every browser out
  version               print the version

Client commands use the token in <data-dir>/api-token and accept
--server host:port, --data-dir and --config.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run executes the command named in args and returns the process exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runServe(ctx, args[1:], stdout, stderr)
	case "repo":
		return runRepo(args[1:], stdout, stderr)
	case "submit":
		return runSubmit(args[1:], stdin, stdout, stderr)
	case "change":
		return runChange(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "pipeline":
		return runPipeline(args[1:], stdout, stderr)
	case "import":
		return runImport(args[1:], stdout, stderr)
	case "alert":
		return runAlert(args[1:], stdout, stderr)
	case "login":
		return runLogin(args[1:], stdout, stderr)
	case "logout":
		return runLogout(args[1:], stdout, stderr)
	case "events":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runEvents(ctx, args[1:], stdout, stderr)
	case "review":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runReview(ctx, args[1:], stdout, stderr)
	case "mcp":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runMCP(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "plumbix %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "plumbix: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
