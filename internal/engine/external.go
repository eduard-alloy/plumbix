package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/scheduler"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// MaxReviewerName bounds the name a reviewer gives itself.
const MaxReviewerName = 100

// reviewPoll is how often a waiting external step looks for its verdict.
var reviewPoll = time.Second

// waitExternal runs an external validator step (WU-023): it publishes the
// review brief, built from the same inputs a container validator gets, and
// waits for the verdict until ctx ends at the step's timeout. The verdict
// becomes the step's verdict.json, so the usual checks apply afterwards. A
// result with no status lets the scheduler record the timeout or cancel.
func (e *Engine) waitExternal(ctx context.Context, r run.Run, c change.Change, p prepared, work string, st pipeline.Step, log io.Writer) scheduler.StepResult {
	errored := func(err error) scheduler.StepResult {
		fmt.Fprintf(log, "external review could not be published: %v\n", err)
		return scheduler.StepResult{Status: scheduler.Errored, Err: err}
	}
	dir := filepath.Join(work, "validators", st.Name)
	nonce, err := randomHex(8)
	if err != nil {
		return errored(err)
	}
	prompt, err := review.BuildPrompt(filepath.Join(dir, "in"), p.workspace, nonce)
	if err != nil {
		return errored(err)
	}
	repo, err := e.store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return errored(err)
	}
	tools, model := review.ToolsReadOnly, ""
	if e.cfg.Settings != nil {
		if tools, err = e.cfg.Settings.AITools(ctx); err != nil {
			return errored(fmt.Errorf("read tools setting: %w", err))
		}
		if model, err = e.cfg.Settings.AIModel(ctx); err != nil {
			return errored(fmt.Errorf("read model setting: %w", err))
		}
	}
	deadline, _ := ctx.Deadline()
	ids := review.CriterionIDs(c.AcceptanceCriteria)
	b := review.Brief{ID: change.NewID("rv_"), RunID: r.ID, ChangeID: c.ID, RepoID: c.RepoID, RepoURL: repo.URL, Step: st.Name, Iteration: r.Iteration,
		Head: p.head, Base: p.base, ExpiresAt: deadline, Tools: tools, Model: model, Prompt: prompt, Schema: review.OutputSchema(ids), Criteria: ids}
	raw, err := json.Marshal(b)
	if err != nil {
		return errored(err)
	}
	now := e.now().UTC()
	if err := e.store.CreateReview(ctx, review.Review{ID: b.ID, RunID: r.ID, RepoID: c.RepoID, ChangeID: c.ID, Step: st.Name, Brief: raw, CreatedAt: now, ExpiresAt: deadline}); err != nil {
		return errored(err)
	}
	defer e.forgetReviewLog(b.ID)
	fmt.Fprintf(log, "waiting for an external reviewer: review %s, until %s. A reviewer takes it with plumbix review --watch or GET /api/v1/reviews/next.\n", b.ID, deadline.UTC().Format(time.RFC3339))
	e.log.Info("external review published", "run", r.ID, "step", st.Name, "review", b.ID)

	ticker := time.NewTicker(reviewPoll)
	defer ticker.Stop()
	holder := "" // the reviewer last written to the log
	silent := false
	// warn raises the repository's no_reviewer alert while nobody who could
	// answer has polled recently, and writes the hint once per silence.
	warn := func() {
		if e.warnNoReviewer(ctx, c.RepoID, c.ID, r.ID, b.ID, st.Name) {
			if !silent {
				fmt.Fprintf(log, "no reviewer has polled in the last %s; run plumbix review --watch somewhere with Claude Code (an alert was raised)\n", presenceWindow)
			}
			silent = true
		} else {
			silent = false
		}
	}
	warn()
	for {
		select {
		case <-ctx.Done():
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// A verdict posted during the last poll interval was accepted
			// with 200, so a step that merely ran out of time still takes
			// it; a cancelled run keeps nothing.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				if rv, err := e.store.GetReview(bg, b.ID); err == nil && rv.Status == review.Done {
					return e.takeVerdict(dir, rv, log)
				}
			}
			if err := e.store.ExpireReview(bg, b.ID, e.now().UTC()); err != nil {
				e.log.Warn("expire review", "review", b.ID, "err", err)
			}
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
				fmt.Fprintf(log, "no verdict arrived by %s; the step timed out and counts as a validator error\n", deadline.UTC().Format(time.RFC3339))
			} else {
				fmt.Fprintln(log, "the run was cancelled before a verdict arrived")
			}
			return scheduler.StepResult{}
		case <-ticker.C:
		}
		rv, err := e.store.GetReview(ctx, b.ID)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return errored(err)
		}
		// The log follows the holder: a claim, a release (the claim went
		// back because the reviewer never got the brief), a new claim. A
		// claim and its verdict can land within one poll, so the claim line
		// is written whenever the holder is new.
		if rv.Status == review.Pending {
			warn()
		}
		switch {
		case holder != "" && rv.Status == review.Pending:
			fmt.Fprintf(log, "the claim by %s was released; waiting for a reviewer again\n", holder)
			holder = ""
		case rv.Reviewer != "" && rv.Reviewer != holder && (rv.Status == review.Claimed || rv.Status == review.Done):
			holder = rv.Reviewer
			fmt.Fprintf(log, "claimed by %s at %s\n", rv.Reviewer, rv.ClaimedAt.UTC().Format(time.RFC3339))
		}
		if rv.Status == review.Done {
			return e.takeVerdict(dir, rv, log)
		}
	}
}

// takeVerdict writes a posted verdict as the step's verdict.json.
func (e *Engine) takeVerdict(dir string, rv review.Review, log io.Writer) scheduler.StepResult {
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return scheduler.StepResult{Status: scheduler.Errored, Err: err}
	}
	if err := os.WriteFile(filepath.Join(out, validation.VerdictFile), rv.Verdict, 0o644); err != nil {
		return scheduler.StepResult{Status: scheduler.Errored, Err: err}
	}
	fmt.Fprintf(log, "verdict posted by %s at %s\n", rv.Reviewer, rv.FinishedAt.UTC().Format(time.RFC3339))
	// The container validator printed its verdict as its last line; the
	// external step does the same from the posted verdict: the verdict line,
	// then the rest of the summary and each finding and risk, so the log
	// alone says what the reviewer found.
	var v validation.Verdict
	if json.Unmarshal(rv.Verdict, &v) == nil {
		writeVerdictNotes(log, v)
	}
	return scheduler.StepResult{Status: scheduler.Succeeded}
}

// writeVerdictNotes prints a posted verdict for a step log: one verdict line
// with the summary's first line and the usage, the remaining summary lines
// indented, then "finding <severity>:" and "risk <area>:" lines.
func writeVerdictNotes(log io.Writer, v validation.Verdict) {
	summary := strings.TrimSpace(v.Summary)
	first, rest := summary, ""
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		first, rest = strings.TrimSpace(summary[:i]), strings.TrimSpace(summary[i+1:])
	}
	fmt.Fprintf(log, "verdict: %s — %s%s\n", v.Outcome, first, usageNote(v.Usage))
	for _, line := range strings.Split(rest, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fmt.Fprintf(log, "  %s\n", line)
		}
	}
	for _, f := range v.Findings {
		where := f.File
		if where != "" && f.Line != nil {
			where = fmt.Sprintf("%s:%d", f.File, *f.Line)
		}
		if where != "" {
			where = " (" + where + ")"
		}
		fmt.Fprintf(log, "finding %s: %s%s\n", f.Severity, strings.Join(strings.Fields(f.Message), " "), where)
	}
	for _, r := range v.Risks {
		human := ""
		if r.HumanRequired {
			human = ", a person must look"
		}
		fmt.Fprintf(log, "risk %s: %s%s\n", r.Area, strings.Join(strings.Fields(r.Message), " "), human)
	}
}

// usageNote renders a verdict's usage for the step log: model and tokens,
// plus turns and cost when the reviewer reported them.
func usageNote(u *validation.Usage) string {
	if u == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("%s, %d input and %d output tokens", u.Model, u.InputTokens, u.OutputTokens)}
	if u.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", u.Turns))
	}
	if u.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f estimated", u.CostUSD))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// ClaimReview hands the next waiting review to reviewer with a fresh token,
// or ok false when nothing waits. repoID narrows to one repository.
func (e *Engine) ClaimReview(ctx context.Context, repoID, reviewer string) (review.Brief, bool, error) {
	reviewer = strings.TrimSpace(reviewer)
	switch {
	case reviewer == "":
		return review.Brief{}, false, &change.InvalidError{Problems: []change.Problem{{Field: "reviewer", Message: "is required: a name for the log and for the rule that nobody reviews their own change"}}}
	case len(reviewer) > MaxReviewerName:
		return review.Brief{}, false, &change.InvalidError{Problems: []change.Problem{{Field: "reviewer", Message: fmt.Sprintf("is longer than %d characters", MaxReviewerName)}}}
	}
	e.noteReviewer(ctx, reviewer, repoID)
	token, err := randomHex(32)
	if err != nil {
		return review.Brief{}, false, err
	}
	rv, ok, err := e.store.ClaimReview(ctx, repoID, reviewer, hashToken(token), e.now().UTC())
	if err != nil || !ok {
		return review.Brief{}, false, err
	}
	var b review.Brief
	if err := json.Unmarshal(rv.Brief, &b); err != nil {
		return review.Brief{}, false, fmt.Errorf("review %s: brief: %w", rv.ID, err)
	}
	b.Token = token
	e.log.Info("review claimed", "review", rv.ID, "reviewer", reviewer)
	return b, true, nil
}

// ReleaseReview gives a claimed review back when its reviewer never got the
// brief, so the claim does not sit unanswered until the step times out.
func (e *Engine) ReleaseReview(ctx context.Context, id string) error {
	if err := e.store.ReleaseReview(ctx, id); err != nil {
		return err
	}
	e.log.Info("review released: the reviewer went away before the brief reached it", "review", id)
	return nil
}

// PostReviewVerdict accepts a reviewer's verdict for a claimed review: the
// token must be the one handed out with the brief, and the verdict must
// pass the checks every verdict passes. The waiting step picks it up.
func (e *Engine) PostReviewVerdict(ctx context.Context, id, token string, raw []byte) error {
	rv, err := e.store.GetReview(ctx, id)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return fmt.Errorf("%w: review %s", change.ErrNotFound, id)
		}
		return err
	}
	// The claim and its token come first, so a stranger learns nothing
	// about the verdict's problems and gets the same 409 for every attempt.
	if rv.Status != review.Claimed || rv.TokenHash != hashToken(token) || !rv.ExpiresAt.After(e.now()) {
		return fmt.Errorf("%w: review %s is %s, or the token is not the one handed out with its brief", change.ErrConflict, id, rv.Status)
	}
	c, err := e.store.GetChange(ctx, rv.ChangeID)
	if err != nil {
		return err
	}
	if _, err := validation.ParseVerdict(raw, c.AcceptanceCriteria); err != nil {
		return err
	}
	if err := e.store.PostReviewVerdict(ctx, id, hashToken(token), json.RawMessage(raw), e.now().UTC()); err != nil {
		if errors.Is(err, change.ErrConflict) {
			return fmt.Errorf("%w: review %s is %s, or the token is not the one handed out with its brief", change.ErrConflict, id, rv.Status)
		}
		return err
	}
	e.log.Info("review verdict posted", "review", id, "reviewer", rv.Reviewer)
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Bounds on reviewer progress lines (WU-027).
const (
	MaxReviewLogLine  = 300 // characters per line
	MaxReviewLogLines = 300 // lines per review
)

// ReviewLogMinInterval is the least time between two lines of one review.
// It is a variable so tests can lower or raise it.
var ReviewLogMinInterval = 500 * time.Millisecond

// ErrTooManyLines means a reviewer exceeded the line cap or the rate.
var ErrTooManyLines = errors.New("too many progress lines")

type reviewLogState struct {
	count int
	last  time.Time
}

// forgetReviewLog drops a review's line counters once its step no longer
// waits (answered, expired or cancelled), so the map does not grow with
// every review the server ever published.
func (e *Engine) forgetReviewLog(id string) {
	e.reviewLogMu.Lock()
	delete(e.reviewLogs, id)
	e.reviewLogMu.Unlock()
}

// AppendReviewLog appends one progress line from the reviewer holding the
// claim to the waiting step's log, prefixed "reviewer:". Newlines are
// folded so one call is one line. The review must be claimed with token.
func (e *Engine) AppendReviewLog(ctx context.Context, id, token, line string) error {
	line = strings.Join(strings.Fields(line), " ")
	switch {
	case line == "":
		return &change.InvalidError{Problems: []change.Problem{{Field: "line", Message: "is empty"}}}
	case utf8.RuneCountInString(line) > MaxReviewLogLine:
		return &change.InvalidError{Problems: []change.Problem{{Field: "line", Message: fmt.Sprintf("is longer than %d characters", MaxReviewLogLine)}}}
	}
	rv, err := e.store.GetReview(ctx, id)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return fmt.Errorf("%w: review %s", change.ErrNotFound, id)
		}
		return err
	}
	if rv.Status != review.Claimed || rv.TokenHash != hashToken(token) {
		return fmt.Errorf("%w: review %s is %s, or the token is not the one handed out with its brief", change.ErrConflict, id, rv.Status)
	}
	now := e.now()
	e.reviewLogMu.Lock()
	if e.reviewLogs == nil {
		e.reviewLogs = map[string]*reviewLogState{}
	}
	st := e.reviewLogs[id]
	if st == nil {
		st = &reviewLogState{}
		e.reviewLogs[id] = st
	}
	if st.count >= MaxReviewLogLines || (st.count > 0 && now.Sub(st.last) < ReviewLogMinInterval) {
		e.reviewLogMu.Unlock()
		return fmt.Errorf("%w: at most %d lines per review and one every %s", ErrTooManyLines, MaxReviewLogLines, ReviewLogMinInterval)
	}
	st.count++
	st.last = now
	e.reviewLogMu.Unlock()
	f, err := os.OpenFile(e.LogPath(rv.RunID, rv.Step), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "reviewer: %s\n", line)
	return err
}
