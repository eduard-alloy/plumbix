package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// Input format versions.
const (
	InputVersion      = "validator-input/v1"
	StepStatusVersion = "step-status/v1"
)

// Paths inside a validator container. WorkspacePath must equal
// runner.WorkspacePath; a runner test checks this.
const (
	InPath        = "/plumbix/in"
	OutPath       = "/plumbix/out"
	WorkspacePath = "/workspace"
)

// File names inside the input and output directories.
const (
	ChangeFile  = "change.json"
	EvidenceDir = "evidence"
	StatusFile  = "status.json"
	LogFile     = "log.txt"
	VerdictFile = "verdict.json"
	DiffFile    = "diff.patch" // optional unified diff from base to head
)

// Input is the content of /plumbix/in/change.json.
type Input struct {
	Plumbix          string      `json:"plumbix"`
	Change           InputChange `json:"change"`
	Commits          Commits     `json:"commits"`
	ChangedFiles     []string    `json:"changed_files"`
	Iteration        int         `json:"iteration"`
	PreviousFindings []Finding   `json:"previous_findings"`
	Guidance         *Guidance   `json:"guidance,omitempty"` // operator instructions, set by Plumbix
}

// Guidance is what the Plumbix operator asks validators to pay attention
// to, globally and for this repository. It comes from settings, never from
// the repository, so validators may follow it.
type Guidance struct {
	Global     string `json:"global,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// InputChange is the part of a change a validator needs.
type InputChange struct {
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	Intent             string             `json:"intent"`
	SourceBranch       string             `json:"source_branch"`
	TargetBranch       string             `json:"target_branch"`
	AcceptanceCriteria []change.Criterion `json:"acceptance_criteria"`
	Constraints        []string           `json:"constraints"`
}

// Commits are the commits a validation compares.
type Commits struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// StepStatus is the content of /plumbix/in/evidence/<step>/status.json.
type StepStatus struct {
	Plumbix        string `json:"plumbix"`
	Step           string `json:"step"`
	Status         string `json:"status"`
	ExitCode       int    `json:"exit_code"`
	DurationMS     int64  `json:"duration_ms"`
	Attempts       int    `json:"attempts,omitempty"`        // above one after retries
	AllowedFailure bool   `json:"allowed_failure,omitempty"` // the failure was tolerated
	Reason         string `json:"reason,omitempty"`          // why a skipped step did not run
}

// Reasons a step was skipped, as written in status.json.
const (
	ReasonNeeds = "needs"
	ReasonPaths = "paths"
)

// StepEvidence is one earlier step's outcome and log.
type StepEvidence struct {
	Step           string
	Status         string
	ExitCode       int
	DurationMS     int64
	Attempts       int
	AllowedFailure bool
	Reason         string
	Log            []byte
}

// NewInput builds a validator input for one iteration of a change.
func NewInput(c change.Change, base, head string, changedFiles []string, iteration int, previous []Finding) Input {
	return Input{
		Plumbix: InputVersion,
		Change: InputChange{
			ID: c.ID, Title: c.Title, Intent: c.Intent,
			SourceBranch: c.SourceBranch, TargetBranch: c.TargetBranch,
			AcceptanceCriteria: c.AcceptanceCriteria, Constraints: nonNil(c.Constraints),
		},
		Commits:          Commits{Base: base, Head: head},
		ChangedFiles:     nonNil(changedFiles),
		Iteration:        iteration,
		PreviousFindings: nonNilFindings(previous),
	}
}

// PrepareInputs writes in and the evidence of earlier steps into dir, which
// is later mounted read-only at /plumbix/in.
func PrepareInputs(dir string, in Input, evidence []StepEvidence) error {
	for _, e := range evidence {
		if !pipeline.ValidStepName(e.Step) {
			return fmt.Errorf("evidence for invalid step name %q", e.Step)
		}
	}
	if err := writeJSON(filepath.Join(dir, ChangeFile), in); err != nil {
		return err
	}
	for _, e := range evidence {
		stepDir := filepath.Join(dir, EvidenceDir, e.Step)
		status := StepStatus{Plumbix: StepStatusVersion, Step: e.Step, Status: e.Status, ExitCode: e.ExitCode, DurationMS: e.DurationMS, AllowedFailure: e.AllowedFailure, Reason: e.Reason}
		if e.Attempts > 1 {
			status.Attempts = e.Attempts
		}
		if err := writeJSON(filepath.Join(stepDir, StatusFile), status); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stepDir, LogFile), e.Log, 0o644); err != nil {
			return fmt.Errorf("write log for %s: %w", e.Step, err)
		}
	}
	return nil
}

// WriteDiff stores the unified diff between the base and head commits.
func WriteDiff(dir string, diff []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, DiffFile), diff, 0o644); err != nil {
		return fmt.Errorf("write diff: %w", err)
	}
	return nil
}

// ReadInput reads change.json from an input directory.
func ReadInput(dir string) (Input, error) {
	var in Input
	data, err := os.ReadFile(filepath.Join(dir, ChangeFile))
	if err != nil {
		return Input{}, fmt.Errorf("read validator input: %w", err)
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return Input{}, fmt.Errorf("parse validator input: %w", err)
	}
	if in.Plumbix != InputVersion {
		return Input{}, fmt.Errorf("validator input version %q, want %q", in.Plumbix, InputVersion)
	}
	return in, nil
}

// ReadStepStatus reads a step's status. ok is false when the step left no
// evidence.
func ReadStepStatus(dir, step string) (StepStatus, bool, error) {
	if !pipeline.ValidStepName(step) {
		return StepStatus{}, false, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, EvidenceDir, step, StatusFile))
	if errors.Is(err, os.ErrNotExist) {
		return StepStatus{}, false, nil
	}
	if err != nil {
		return StepStatus{}, false, fmt.Errorf("read status of %s: %w", step, err)
	}
	var st StepStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return StepStatus{}, false, fmt.Errorf("parse status of %s: %w", step, err)
	}
	return st, true, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilFindings(f []Finding) []Finding {
	if f == nil {
		return []Finding{}
	}
	return f
}
