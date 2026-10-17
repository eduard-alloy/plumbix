//go:build unix

// Package refvalidator is Plumbix's deterministic reference validator. It
// judges each acceptance criterion from pipeline evidence or a shell command,
// and flags changes a human should look at. It never calls a model.
package refvalidator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// DefaultCommandTimeout limits each check command.
const DefaultCommandTimeout = 60 * time.Second

// outputTail is how much command output a reason quotes.
const outputTail = 500

// Options configures a run.
type Options struct {
	CommandTimeout time.Duration
}

// Run reads the validator input in inDir and judges it, running check
// commands in workspace. An error means no verdict could be produced.
func Run(ctx context.Context, inDir, workspace string, o Options) (validation.Verdict, error) {
	in, err := validation.ReadInput(inDir)
	if err != nil {
		return validation.Verdict{}, err
	}
	if o.CommandTimeout <= 0 {
		o.CommandTimeout = DefaultCommandTimeout
	}
	v := validation.Verdict{Plumbix: validation.VerdictVersion, Outcome: validation.Approve}
	counts := map[validation.Status]int{}
	for _, c := range in.Change.AcceptanceCriteria {
		cv, err := assess(ctx, c, inDir, workspace, o)
		if err != nil {
			return validation.Verdict{}, err
		}
		counts[cv.Status]++
		v.Criteria = append(v.Criteria, cv)
		if cv.Status == validation.Unmet {
			v.Outcome = validation.Fix
			v.Findings = append(v.Findings, validation.Finding{
				Severity: "major", Criterion: c.ID,
				Message: fmt.Sprintf("%s is not met (%s): %s", c.ID, c.Text, cv.Reason),
			})
		}
	}
	v.Risks = risks(in.ChangedFiles)
	v.Summary = fmt.Sprintf("%d of %d criteria met, %d unmet, %d not checkable deterministically",
		counts[validation.Met], len(in.Change.AcceptanceCriteria), counts[validation.Unmet], counts[validation.Unknown])
	return v, nil
}

func assess(ctx context.Context, c change.Criterion, inDir, workspace string, o Options) (validation.CriterionVerdict, error) {
	cv := validation.CriterionVerdict{ID: c.ID}
	switch {
	case c.Check == nil:
		cv.Status, cv.Reason = validation.Unknown, "no deterministic check given"
	case len(c.Check.Steps) > 0:
		return assessSteps(cv, c.Check.Steps, inDir)
	default:
		cv.Status, cv.Reason = runCommand(ctx, c.Check.Command, workspace, o.CommandTimeout)
	}
	return cv, nil
}

func assessSteps(cv validation.CriterionVerdict, steps []string, inDir string) (validation.CriterionVerdict, error) {
	var failed, missing, notNeeded []string
	for _, s := range steps {
		st, ok, err := validation.ReadStepStatus(inDir, s)
		if err != nil {
			return cv, err
		}
		switch {
		case !ok:
			missing = append(missing, s)
		case st.Status == "skipped" && st.Reason == validation.ReasonPaths:
			notNeeded = append(notNeeded, s)
		case st.Status != "succeeded":
			failed = append(failed, fmt.Sprintf("%s %s", s, st.Status))
		default:
			cv.Evidence = append(cv.Evidence, path.Join(validation.EvidenceDir, s, validation.LogFile))
		}
	}
	switch {
	case len(failed) > 0:
		cv.Status, cv.Reason = validation.Unmet, "step "+strings.Join(failed, ", ")
	case len(missing) > 0:
		cv.Status, cv.Reason = validation.Unknown, "no evidence for step "+strings.Join(missing, ", ")
	case len(notNeeded) == len(steps):
		cv.Status, cv.Reason = validation.Met, "step "+strings.Join(steps, ", ")+" did not need to run: no changed file matched its paths"
	case len(notNeeded) > 0:
		cv.Status, cv.Reason = validation.Met, "step "+strings.Join(steps, ", ")+" succeeded ("+strings.Join(notNeeded, ", ")+" did not need to run)"
	default:
		cv.Status, cv.Reason = validation.Met, "step "+strings.Join(steps, ", ")+" succeeded"
	}
	return cv, nil
}

// runCommand runs cmd with sh -c in workspace. The whole process group is
// killed at the time limit so child processes can't keep it alive.
func runCommand(ctx context.Context, cmd, workspace string, timeout time.Duration) (validation.Status, string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = workspace
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = time.Second
	err := c.Run()
	tail := strings.TrimSpace(out.String())
	if len(tail) > outputTail {
		tail = "..." + tail[len(tail)-outputTail:]
	}
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return validation.Unmet, fmt.Sprintf("command %q exceeded its %s time limit", cmd, timeout)
	case err == nil:
		return validation.Met, fmt.Sprintf("command %q exited 0", cmd)
	case errors.As(err, &exitErr):
		return validation.Unmet, withOutput(fmt.Sprintf("command %q exited %d", cmd, exitErr.ExitCode()), tail)
	default:
		return validation.Unmet, fmt.Sprintf("command %q could not run: %v", cmd, err)
	}
}

func withOutput(reason, output string) string {
	if output == "" {
		return reason
	}
	return reason + ": " + output
}

func risks(changed []string) []validation.Risk {
	var rs []validation.Risk
	tests := 0
	pipeline := false
	for _, f := range changed {
		if isTestFile(f) {
			tests++
		}
		if strings.HasPrefix(f, validation.PipelineDir) {
			pipeline = true
		}
	}
	if tests > 0 {
		rs = append(rs, validation.Risk{Area: "tests", Message: fmt.Sprintf("%d test files changed; check that tests were not weakened", tests)})
	}
	if pipeline {
		rs = append(rs, validation.Risk{Area: "pipeline", Message: "the change edits " + validation.PipelineDir, HumanRequired: true})
	}
	return rs
}

// isTestFile recognises common test file conventions.
func isTestFile(p string) bool {
	base := path.Base(p)
	for _, dir := range []string{"test/", "tests/", "__tests__/", "spec/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		(strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) ||
		strings.HasSuffix(base, "_test.py")
}
