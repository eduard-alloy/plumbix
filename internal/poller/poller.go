// Package poller notices pushes by asking each repo's remote for its branch
// heads on an interval, and starts the right run for every moved branch.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// maxAlertCommits caps the commits listed in one bypass alert.
const maxAlertCommits = 50

// Token checks.
const (
	tokenCheckInterval = 24 * time.Hour
	tokenWarningPeriod = 7 * 24 * time.Hour
	tokenCheckTimeout  = 30 * time.Second
)

// Engine starts runs and reads remotes.
type Engine interface {
	ListHeads(ctx context.Context, repo change.Repo) (map[string]string, error)
	Validate(ctx context.Context, changeID string) (run.Run, error)
	RunBranch(ctx context.Context, repoID, branch string) (run.Run, error)
	// Commits lists the first-parent commits from..to, newest first; only
	// to itself when from is empty.
	Commits(ctx context.Context, repo change.Repo, from, to string) ([]gitops.Commit, error)
}

// Store is what the poller reads and writes.
type Store interface {
	ListRepos(ctx context.Context) ([]change.Repo, error)
	ListChanges(ctx context.Context, f change.Filter) ([]change.Change, error)
	ListRuns(ctx context.Context, changeID string) ([]run.Run, error)
	KnownHeads(ctx context.Context, repoID string) (map[string]string, bool, error)
	SaveHeads(ctx context.Context, repoID string, heads map[string]string, at time.Time) error
	ListMerges(ctx context.Context, repoID, branch string) ([]change.MergeRecord, error)
	alert.Store
}

// Config configures polling.
type Config struct {
	Interval time.Duration
}

// Poller polls every registered repo.
type Poller struct {
	store  Store
	engine Engine
	hosts  host.Source
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
}

// New returns a poller. hosts may be nil, which skips token expiry checks.
func New(store Store, engine Engine, hosts host.Source, cfg Config, log *slog.Logger) *Poller {
	return &Poller{store: store, engine: engine, hosts: hosts, cfg: cfg, log: log, now: time.Now}
}

// Run polls until ctx ends, and checks host tokens once a day.
func (p *Poller) Run(ctx context.Context) {
	tick := time.NewTicker(p.cfg.Interval)
	defer tick.Stop()
	var lastTokenCheck time.Time
	for {
		if p.now().Sub(lastTokenCheck) >= tokenCheckInterval {
			p.checkTokens(ctx)
			lastTokenCheck = p.now()
		}
		if err := p.PollOnce(ctx); err != nil && ctx.Err() == nil {
			p.log.Warn("poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// PollOnce polls every repo once. A failing repo doesn't stop the others;
// all failures are returned together.
func (p *Poller) PollOnce(ctx context.Context) error {
	repos, err := p.store.ListRepos(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, repo := range repos {
		if err := p.pollRepo(ctx, repo); err != nil {
			errs = append(errs, fmt.Errorf("repo %s: %w", repo.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *Poller) pollRepo(ctx context.Context, repo change.Repo) error {
	heads, err := p.engine.ListHeads(ctx, repo)
	if err != nil {
		return err
	}
	known, polled, err := p.store.KnownHeads(ctx, repo.ID)
	if err != nil {
		return err
	}
	if polled {
		if err := p.react(ctx, repo, heads, known); err != nil {
			return err
		}
	}
	return p.store.SaveHeads(ctx, repo.ID, heads, p.now().UTC())
}

// react starts runs for branches whose head moved since the last poll.
func (p *Poller) react(ctx context.Context, repo change.Repo, heads, known map[string]string) error {
	changes, err := p.store.ListChanges(ctx, change.Filter{RepoID: repo.ID})
	if err != nil {
		return err
	}
	open := map[string]change.Change{}
	for _, c := range changes {
		if !c.State.Final() {
			open[c.SourceBranch] = c
		}
	}
	for branch, sha := range heads {
		if known[branch] == sha {
			continue
		}
		if c, ok := open[branch]; ok {
			if c.State.NeedsPerson() {
				// A change that needs a person is not moved by a push (A21).
				// The new head is still recorded, so the page can show it.
				p.log.Info("push on a change that needs a person; not revalidating", "repo", repo.ID, "branch", branch, "change", c.ID, "state", c.State)
				continue
			}
			if skip, err := p.alreadyCovered(ctx, c.ID, sha); err != nil || skip {
				if err != nil {
					p.log.Warn("read runs", "change", c.ID, "err", err)
				}
				continue
			}
			if _, err := p.engine.Validate(ctx, c.ID); err != nil {
				p.log.Warn("revalidate after push", "change", c.ID, "err", err)
			} else {
				p.log.Info("push detected, revalidating", "repo", repo.ID, "branch", branch, "change", c.ID)
			}
			continue
		}
		if protected(repo, branch) && p.onlyPlumbixCommits(ctx, repo, branch, known[branch], sha) {
			p.log.Info("protected branch moved by a Plumbix merge", "repo", repo.ID, "branch", branch)
			continue
		}
		if _, err := p.engine.RunBranch(ctx, repo.ID, branch); err != nil {
			p.log.Warn("run after push", "repo", repo.ID, "branch", branch, "err", err)
		} else {
			p.log.Info("push detected, running pipeline", "repo", repo.ID, "branch", branch)
		}
	}
	return nil
}

// alreadyCovered reports whether the change's latest run already validates
// sha, or is still running without having fetched yet (it will see sha).
func (p *Poller) alreadyCovered(ctx context.Context, changeID, sha string) (bool, error) {
	runs, err := p.store.ListRuns(ctx, changeID)
	if err != nil || len(runs) == 0 {
		return false, err
	}
	last := runs[len(runs)-1]
	return last.Head == sha || (last.Status == run.Running && last.Head == ""), nil
}

func protected(repo change.Repo, branch string) bool {
	for _, b := range repo.ProtectedBranches {
		if b == branch {
			return true
		}
	}
	return false
}

// onlyPlumbixCommits checks the commits that moved a protected branch from
// known to sha. Commits outside every Plumbix merge are bypasses and raise
// one alert for the push. It reports whether every commit was Plumbix's.
func (p *Poller) onlyPlumbixCommits(ctx context.Context, repo change.Repo, branch, known, sha string) bool {
	commits, err := p.engine.Commits(ctx, repo, known, sha)
	if err != nil && known != "" {
		// The old head may be gone after a force push; judge the tip alone.
		commits, err = p.engine.Commits(ctx, repo, "", sha)
	}
	if err != nil {
		p.log.Warn("list commits for bypass check", "repo", repo.ID, "branch", branch, "err", err)
		return false
	}
	merges, err := p.store.ListMerges(ctx, repo.ID, branch)
	if err != nil {
		p.log.Warn("list merges for bypass check", "repo", repo.ID, "branch", branch, "err", err)
		return false
	}
	byCommit := map[string]change.MergeRecord{}
	for _, m := range merges {
		byCommit[m.Commit] = m
	}
	var foreign []gitops.Commit
	coveredUntil := "" // inside a Plumbix range previous..commit until this sha
	for _, c := range commits {
		if m, ok := byCommit[c.SHA]; ok {
			coveredUntil = m.Previous
			continue
		}
		if coveredUntil != "" && c.SHA != coveredUntil {
			continue
		}
		coveredUntil = ""
		foreign = append(foreign, c)
	}
	if len(foreign) == 0 {
		return true
	}
	p.raiseBypass(ctx, repo, branch, sha, foreign)
	return false
}

func (p *Poller) raiseBypass(ctx context.Context, repo change.Repo, branch, sha string, foreign []gitops.Commit) {
	listed := foreign
	if len(listed) > maxAlertCommits {
		listed = listed[:maxAlertCommits]
	}
	details := make([]map[string]any, 0, len(listed))
	for _, c := range listed {
		details = append(details, map[string]any{"sha": c.SHA, "author": c.Author, "email": c.Email, "time": c.Time.UTC().Format(time.RFC3339), "subject": c.Subject})
	}
	first := foreign[len(foreign)-1] // oldest, the one that left the verdict behind
	msg := fmt.Sprintf("commit %s by %s reached %s without a verdict", shortSHA(first.SHA), first.Author, branch)
	if n := len(foreign) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	a, created, err := p.store.Raise(ctx, alert.Alert{
		Kind: alert.BypassCommit, Key: branch + "@" + sha, RepoID: repo.ID, Message: msg,
		Details: map[string]any{"branch": branch, "head": sha, "count": len(foreign), "commits": details}, CreatedAt: p.now(),
	})
	if err != nil {
		p.log.Error("raise bypass alert", "repo", repo.ID, "branch", branch, "err", err)
		return
	}
	if created {
		p.log.Warn("bypass commit", "repo", repo.ID, "branch", branch, "alert", a.ID, "msg", msg)
	}
}

// checkTokens raises an alert for tokens that expire soon or whose check
// failed, and resolves it once the token is healthy again.
func (p *Poller) checkTokens(ctx context.Context) {
	if p.hosts == nil {
		return
	}
	for kind, h := range p.hosts.Hosts() {
		cctx, cancel := context.WithTimeout(ctx, tokenCheckTimeout)
		exp, ok, err := h.TokenExpiry(cctx)
		cancel()
		var a alert.Alert
		switch {
		case err != nil:
			a = alert.Alert{Message: fmt.Sprintf("%s token check failed: %v", kind, err)}
		case ok && exp.Sub(p.now()) < tokenWarningPeriod:
			a = alert.Alert{Message: fmt.Sprintf("%s token expires on %s", kind, exp.UTC().Format("2006-01-02")), Details: map[string]any{"expires": exp.UTC().Format(time.RFC3339)}}
		default:
			if err := p.store.ResolveKey(ctx, alert.TokenExpiry, kind, p.now()); err != nil {
				p.log.Error("resolve token alert", "host", kind, "err", err)
			}
			continue
		}
		a.Kind, a.Key, a.CreatedAt = alert.TokenExpiry, kind, p.now()
		if a.Details == nil {
			a.Details = map[string]any{}
		}
		a.Details["host"] = kind
		if _, _, err := p.store.Raise(ctx, a); err != nil {
			p.log.Error("raise token alert", "host", kind, "err", err)
		}
		p.log.Warn("host token", "host", kind, "msg", a.Message)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
