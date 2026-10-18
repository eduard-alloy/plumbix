// Package reviewer is plumbix review --watch: it pulls review briefs from a
// Plumbix server, runs Claude Code non-interactively on a checkout of the
// change, and posts the verdict (WU-023, A26). It uses whatever
// authentication Claude Code already has on this machine.
package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/claudecode"
	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Defaults for a review run.
const (
	DefaultWait     = 50 // seconds a claim waits for a brief
	DefaultMaxTurns = claudecode.DefaultMaxTurns
	retryAfterError = 5 * time.Second
	// defaultProgressEvery keeps progress posts under the server's rate.
	defaultProgressEvery = 600 * time.Millisecond
	// task is the short prompt; the brief's user prompt arrives on stdin,
	// which has no size limit, where a command-line argument has.
)

// Options configure Run.
type Options struct {
	Client   *client.Client
	Reviewer string // the name the server logs and uses for the own-change rule
	RepoID   string // only this repository's reviews, when set
	Once     bool   // return after one review instead of looping
	Tools    string // overrides the brief's tool policy when set
	Model    string // overrides the brief's model when set
	Claude   string // the claude executable; "claude" on the PATH by default
	WorkDir  string // mirrors and worktrees live under here
	MaxTurns int
	// MaxBudgetUSD caps one review's spend as Claude Code estimates it; zero sets no cap.
	MaxBudgetUSD float64
	Wait         int // long-poll seconds per claim
	Stdout       io.Writer
	Stderr       io.Writer
	// progressEvery is the least time between two progress lines posted
	// to the server; zero means the default, a negative value no limit
	// (tests).
	progressEvery time.Duration
}

// Run claims reviews and answers them until ctx ends, or after one review
// with Once. Errors answering a review are reported and, unless Once, do
// not stop the loop: the step times out on the server and a person sees it.
func Run(ctx context.Context, o Options) error {
	if o.Client == nil || o.Reviewer == "" || o.WorkDir == "" {
		return errors.New("reviewer: a client, a reviewer name and a work directory are required")
	}
	if o.Claude == "" {
		o.Claude = "claude"
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	if o.Wait == 0 {
		o.Wait = DefaultWait
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.progressEvery == 0 {
		o.progressEvery = defaultProgressEvery
	}
	for {
		b, ok, err := next(ctx, o)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(o.Stderr, "plumbix review: %v\n", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(retryAfterError):
			}
			continue
		}
		if !ok {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		err = reviewOne(ctx, o, b)
		if err != nil {
			fmt.Fprintf(o.Stderr, "plumbix review: %s: %v\n", b.ID, err)
		}
		if o.Once {
			return err
		}
	}
}

// next claims a brief, long-polling once. ok is false when nothing waits.
func next(ctx context.Context, o Options) (review.Brief, bool, error) {
	q := url.Values{"reviewer": {o.Reviewer}, "wait": {strconv.Itoa(o.Wait)}}
	if o.RepoID != "" {
		q.Set("repo_id", o.RepoID)
	}
	// The server holds the request up to wait seconds; the request timeout
	// of a plain call is shorter and would abandon a claim already made.
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(o.Wait)*time.Second+15*time.Second)
	defer cancel()
	resp, err := o.Client.Wait(pollCtx, http.MethodGet, "/api/v1/reviews/next?"+q.Encode(), nil)
	if err != nil {
		return review.Brief{}, false, err
	}
	if resp.Status == http.StatusNoContent {
		return review.Brief{}, false, nil
	}
	if !resp.OK() {
		return review.Brief{}, false, resp.Error()
	}
	var b review.Brief
	if err := json.Unmarshal(resp.Body, &b); err != nil {
		return review.Brief{}, false, fmt.Errorf("decode brief: %w", err)
	}
	return b, true, nil
}

// reviewOne checks the head out, runs claude on it, and posts the verdict.
func reviewOne(ctx context.Context, o Options, b review.Brief) error {
	wt, cleanup, err := checkout(ctx, o.WorkDir, b)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Fprintf(o.Stdout, "review %s: %s %s, iteration %d, head %s\n", b.ID, b.ChangeID, b.Step, b.Iteration, short(b.Head))

	// Progress lines go to the step log while Claude works, so the change
	// page shows what is being read and run.
	var lastPost time.Time
	progress := func(line string) {
		if o.progressEvery > 0 && time.Since(lastPost) < o.progressEvery {
			return
		}
		lastPost = time.Now()
		body, _ := json.Marshal(map[string]string{"token": b.Token, "line": line})
		if resp, err := o.Client.Do(ctx, http.MethodPost, "/api/v1/reviews/"+url.PathEscape(b.ID)+"/log", bytes.NewReader(body)); err == nil && !resp.OK() && resp.Status != http.StatusTooManyRequests {
			fmt.Fprintf(o.Stderr, "plumbix review: %s: progress line refused: %v\n", b.ID, resp.Error())
		}
	}
	tools := b.Tools
	if o.Tools != "" {
		tools = o.Tools
	}
	model := b.Model
	if o.Model != "" {
		model = o.Model
	}
	res, err := claudecode.Run(ctx, claudecode.Options{Claude: o.Claude, Dir: wt, System: b.Prompt.System, User: b.Prompt.User, Schema: b.Schema,
		Tools: tools, Model: model, MaxTurns: o.MaxTurns, MaxBudgetUSD: o.MaxBudgetUSD, Deadline: b.ExpiresAt, Progress: progress})
	if err != nil {
		return err
	}
	criteria := make([]change.Criterion, 0, len(b.Criteria))
	for _, id := range b.Criteria {
		criteria = append(criteria, change.Criterion{ID: id})
	}
	if model == "" {
		model = res.Model()
	}
	if model == "" {
		model = "claude-code"
	}
	v, err := review.ToVerdict(res.StructuredOutput, criteria, validation.Usage{Model: model, InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, Turns: res.NumTurns, CostUSD: res.TotalCostUSD})
	if err != nil {
		return fmt.Errorf("claude's answer is not a verdict Plumbix accepts: %w", err)
	}
	body, _ := json.Marshal(struct {
		Token   string             `json:"token"`
		Verdict validation.Verdict `json:"verdict"`
	}{b.Token, v})
	resp, err := o.Client.Do(ctx, http.MethodPost, "/api/v1/reviews/"+url.PathEscape(b.ID)+"/verdict", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post verdict: %w", err)
	}
	if !resp.OK() {
		return fmt.Errorf("post verdict: %w", resp.Error())
	}
	fmt.Fprintf(o.Stdout, "review %s: %s · %d turns · $%.2f · %s\n", b.ID, v.Outcome, res.NumTurns, res.TotalCostUSD, firstLine(v.Summary))
	return nil
}

// checkout materialises the brief's head in a worktree under workDir, with
// a bare mirror per repository that is fetched before each review. The
// user's own git access applies. cleanup removes the worktree.
func checkout(ctx context.Context, workDir string, b review.Brief) (string, func(), error) {
	mirror := filepath.Join(workDir, "repos", b.RepoID+".git")
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	if _, err := os.Stat(mirror); err != nil {
		if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
			return "", nil, err
		}
		if err := git("clone", "--quiet", "--bare", b.RepoURL, mirror); err != nil {
			return "", nil, err
		}
	}
	if err := git("-C", mirror, "fetch", "--quiet", "--prune", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		return "", nil, err
	}
	if err := git("-C", mirror, "cat-file", "-e", b.Head+"^{commit}"); err != nil {
		return "", nil, fmt.Errorf("head %s is not on %s after a fetch: %w", short(b.Head), b.RepoURL, err)
	}
	wt := filepath.Join(workDir, "worktrees", b.ID)
	if err := git("-C", mirror, "worktree", "add", "--quiet", "--detach", wt, b.Head); err != nil {
		return "", nil, err
	}
	cleanup := func() {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = exec.CommandContext(bg, "git", "-C", mirror, "worktree", "remove", "--force", wt).Run()
	}
	return wt, cleanup, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return claudecode.Excerpt(s)
}
