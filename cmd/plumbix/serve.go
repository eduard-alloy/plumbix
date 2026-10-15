package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/eduard-alloy/plumbix/internal/api"
	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/config"
)

// runServe parses serve flags, loads config and runs the server until ctx ends.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var over config.Overrides
	fs.StringVar(&over.ConfigPath, "config", "", "config file (default <data-dir>/config.yaml)")
	fs.StringVar(&over.DataDir, "data-dir", "", "data directory (default ~/.plumbix)")
	fs.StringVar(&over.Listen, "listen", "", "listen address, loopback only (default "+config.DefaultListen+")")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: find home directory: %v\n", err)
		return 1
	}
	cfg, err := config.Load(over, os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
		return 1
	}
	if err := config.ValidateListen(cfg.Server.Listen); err != nil {
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
		return 1
	}

	token, err := auth.EnsureToken(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "plumbix: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		logger.Error("listen failed", "addr", cfg.Server.Listen, "err", err)
		return 1
	}
	if err := api.Run(ctx, ln, api.NewHandler(version, token), logger, config.DefaultShutdownTimeout); err != nil {
		logger.Error("server stopped with error", "err", err)
		return 1
	}
	return 0
}
