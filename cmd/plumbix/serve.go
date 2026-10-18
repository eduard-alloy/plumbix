package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/eduard-alloy/plumbix/internal/api"
	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/config"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/poller"
	"github.com/eduard-alloy/plumbix/internal/runner"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
	"github.com/eduard-alloy/plumbix/internal/store"
	"github.com/eduard-alloy/plumbix/web"
)

// DatabaseFile is the SQLite database's file name inside the data directory.
const DatabaseFile = "plumbix.db"

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
	db, err := store.Open(filepath.Join(cfg.DataDir, DatabaseFile))
	if err != nil {
		logger.Error("open database failed", "err", err)
		return 1
	}
	defer db.Close()
	// Events are written by the store; the hub fans them out to open
	// streams and ends them when the server stops.
	hub := events.NewHub(db)
	db.OnEvent(hub.Publish)
	go func() { <-ctx.Done(); hub.Close() }()

	// Secrets are read through the manager on every use, so a token set in
	// the browser needs no restart.
	secretStore, err := secrets.Open(cfg.SecretsFile)
	if err != nil {
		logger.Error("load secrets failed", "err", err)
		return 1
	}
	prefs := settings.New(db)
	if err := prefs.EnsureReviewers(context.Background()); err != nil {
		logger.Error("reviewer profiles failed", "err", err)
		return 1
	}
	docker, err := runner.NewDocker()
	if err != nil {
		logger.Error("docker client failed", "err", err)
		return 1
	}
	defer docker.Close()
	hosts := host.Live(secretStore.Values)
	eng := engine.New(db, docker, engine.Config{
		DataDir: cfg.DataDir, MaxParallel: cfg.Runner.MaxParallel, DefaultTimeout: cfg.Runner.DefaultTimeout,
		CPUs: cfg.Runner.StepCPUs, MemoryMB: cfg.Runner.StepMemoryMB, MaxFixRounds: cfg.FixRounds.Max,
		SecretSource: secretStore.Values, Hosts: hosts, Settings: prefs,
	}, logger)
	if err := eng.Recover(ctx); err != nil {
		logger.Error("recover interrupted runs failed", "err", err)
		return 1
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), config.DefaultShutdownTimeout)
		defer cancel()
		eng.Shutdown(stopCtx)
	}()
	if cfg.Poller.Enabled {
		p := poller.New(db, eng, hosts, poller.Config{Interval: cfg.Poller.Interval}, logger)
		pollDone := make(chan struct{})
		go func() { p.Run(ctx); close(pollDone) }()
		defer func() { <-pollDone }()
	}
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		logger.Error("listen failed", "addr", cfg.Server.Listen, "err", err)
		return 1
	}
	// Browsers must use the bound address: login links point there and
	// cookie requests must carry it as Host.
	deps := api.Deps{
		Version: version, Token: token, Changes: change.NewService(db), Engine: eng, Runs: db, Alerts: db, Events: hub, Logger: logger,
		Sessions: auth.NewSessions(db), Origin: "http://" + ln.Addr().String(),
		Web:      web.Handler(web.Dist()),
		Settings: prefs, Secrets: secretStore, Hosts: hosts,
	}
	if err := api.Run(ctx, ln, api.NewHandler(deps), logger, config.DefaultShutdownTimeout); err != nil {
		logger.Error("server stopped with error", "err", err)
		return 1
	}
	return 0
}
