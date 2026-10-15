//go:build unix

// Command reference is Plumbix's deterministic reference validator. It runs
// as a pipeline step: it reads /plumbix/in, judges each acceptance criterion
// from step evidence or a shell command, and writes /plumbix/out/verdict.json.
//
// Environment overrides (mainly for tests): PLUMBIX_IN, PLUMBIX_OUT,
// PLUMBIX_WORKSPACE, PLUMBIX_CHECK_TIMEOUT (a duration, default 60s).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/eduard-alloy/plumbix/internal/refvalidator"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

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
	timeout := refvalidator.DefaultCommandTimeout
	if s := getenv("PLUMBIX_CHECK_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			fmt.Fprintf(stderr, "reference validator: PLUMBIX_CHECK_TIMEOUT %q is not a positive duration\n", s)
			return 1
		}
		timeout = d
	}
	v, err := refvalidator.Run(context.Background(), or("PLUMBIX_IN", validation.InPath), or("PLUMBIX_WORKSPACE", validation.WorkspacePath), refvalidator.Options{CommandTimeout: timeout})
	if err != nil {
		fmt.Fprintf(stderr, "reference validator: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "reference validator: %v\n", err)
		return 1
	}
	path := filepath.Join(or("PLUMBIX_OUT", validation.OutPath), validation.VerdictFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintf(stderr, "reference validator: write verdict: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "reference validator: %s: %s\n", v.Outcome, v.Summary)
	return 0
}
