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
  serve     start the Plumbix server (local addresses only)
  version   print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command named in args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runServe(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "plumbix %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "plumbix: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
