// Command ai is Plumbix's AI validator. It runs as a pipeline step: it reads
// /plumbix/in and the workspace, asks Claude to judge the change against its
// intent and acceptance criteria, and writes /plumbix/out/verdict.json.
//
// Environment: ANTHROPIC_API_KEY (required, pass it as a step secret),
// PLUMBIX_AI_MODEL (default claude-opus-5-5), PLUMBIX_AI_EFFORT (low, medium,
// high, xhigh or max; default high). PLUMBIX_IN, PLUMBIX_OUT and
// PLUMBIX_WORKSPACE override the container paths.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/eduard-alloy/plumbix/internal/aivalidator"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

var efforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

func main() {
	os.Exit(run(os.Getenv, os.Stderr))
}

// run validates and writes the verdict. A non-zero exit means no verdict was
// written, which Plumbix treats as a validator error.
func run(getenv func(string) string, stderr io.Writer, opts ...option.RequestOption) int {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	cfg := aivalidator.Config{
		APIKey: getenv("ANTHROPIC_API_KEY"),
		Model:  or("PLUMBIX_AI_MODEL", aivalidator.DefaultModel),
		Effort: or("PLUMBIX_AI_EFFORT", aivalidator.DefaultEffort),
	}
	if !efforts[cfg.Effort] {
		fmt.Fprintf(stderr, "ai validator: PLUMBIX_AI_EFFORT %q must be low, medium, high, xhigh or max\n", cfg.Effort)
		return 1
	}
	v, err := aivalidator.Validate(context.Background(), cfg, or("PLUMBIX_IN", validation.InPath), or("PLUMBIX_WORKSPACE", validation.WorkspacePath), opts...)
	if err != nil {
		fmt.Fprintf(stderr, "ai validator: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "ai validator: %v\n", err)
		return 1
	}
	path := filepath.Join(or("PLUMBIX_OUT", validation.OutPath), validation.VerdictFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintf(stderr, "ai validator: write verdict: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "ai validator: %s: %s (%s, %d input and %d output tokens)\n", v.Outcome, v.Summary, v.Usage.Model, v.Usage.InputTokens, v.Usage.OutputTokens)
	return 0
}
