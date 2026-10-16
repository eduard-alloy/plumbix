// Package poller notices pushes by asking each repo's remote for its branch
// heads on an interval, and starts the right run for every moved branch.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

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
}

// Store is what the poller reads and writes.
type Store interface {
	ListRepos(ctx context.Context) ([]change.Repo, error)
	ListChanges(ctx context.Context, f change.Filter) ([]change.Change, error)
	ListRuns(ctx context.Context, changeID string) ([]run.Run, error)
	KnownHeads(ctx context.Context, repoID string) (map[string]string, bool, error)
	SaveHeads(ctx context.Context, repoID string, heads map[string]string, at time.Time) error
}

// Config configures polling.
type Config struct {
	Interval time.Duration
}

// Poller polls every registered repo.
type Poller struct {
	store  Store
	engine Engine
	hosts  host.Registry
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
}

// New returns a poller. hosts may be nil, which skips token expiry checks.
func New(store Store, engine Engine, hosts host.Registry, cfg Config, log *slog.Logger) *Poller {
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

// checkTokens logs a warning for tokens that expire soon or were rejected.
func (p *Poller) checkTokens(ctx context.Context) {
	for kind, h := range p.hosts {
		cctx, cancel := context.WithTimeout(ctx, tokenCheckTimeout)
		exp, ok, err := h.TokenExpiry(cctx)
		cancel()
		switch {
		case err != nil:
			p.log.Warn("host token check failed", "host", kind, "err", err)
		case ok && exp.Sub(p.now()) < tokenWarningPeriod:
			p.log.Warn("host token expires soon", "host", kind, "expires", exp)
		}
	}
}
