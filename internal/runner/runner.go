// Package runner executes single pipeline steps in isolated containers.
package runner

import (
	"context"
	"io"
	"time"
)

// Runner executes one job. A non-zero exit or a timeout is a normal Result.
// An error means the job could not be run properly (engine unavailable,
// image pull failed) or ctx was cancelled.
type Runner interface {
	Run(ctx context.Context, job Job) (Result, error)
}

// Job describes one step execution.
type Job struct {
	Name      string
	Image     string
	Script    string // run with sh -c; empty uses the image's entrypoint
	Env       map[string]string
	Workspace string // host directory mounted read-write at /workspace
	Mounts    []Mount
	Timeout   time.Duration
	CPUs      float64
	MemoryMB  int
	Log       io.Writer // receives stdout and stderr
}

// Mount is an extra host directory made available to the container.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Result is the outcome of a job that ran.
type Result struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
}

// WorkspacePath is where the workspace appears inside the container.
const WorkspacePath = "/workspace"
