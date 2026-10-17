package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
)

// presenceWindow is how recently a reviewer must have polled to count as
// present; a test shortens it.
var presenceWindow = 2 * time.Minute

// Reviewer is an external reviewer the server has heard from since it
// started: every poll of GET /reviews/next is a heartbeat (WU-026).
type Reviewer struct {
	Name     string    `json:"name"`
	RepoID   string    `json:"repo_id,omitempty"` // the poll's repository filter, if any
	LastSeen time.Time `json:"last_seen"`
}

// presence is the engine's memory of reviewers, by name. It is not
// persisted: after a restart nobody has been heard from, which is the truth.
type presence struct {
	mu   sync.Mutex
	seen map[string]Reviewer
}

// note records a poll and reports whether the reviewer counted as present
// before it: false when it is new, or had not polled within the window, or
// now covers a repository it did not cover before.
func (p *presence) note(name, repoID string, at time.Time) (wasPresent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]Reviewer{}
	}
	prev, ok := p.seen[name]
	wasPresent = ok && at.Sub(prev.LastSeen) <= presenceWindow && (prev.RepoID == repoID || prev.RepoID == "")
	p.seen[name] = Reviewer{Name: name, RepoID: repoID, LastSeen: at}
	return wasPresent
}

// list returns reviewers, most recently seen first.
func (p *presence) list() []Reviewer {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Reviewer, 0, len(p.seen))
	for _, r := range p.seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// available reports whether a reviewer whose filter covers repoID polled
// within the window before now.
func (p *presence) available(repoID string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.seen {
		if (r.RepoID == "" || r.RepoID == repoID) && now.Sub(r.LastSeen) <= presenceWindow {
			return true
		}
	}
	return false
}

// Reviewers lists the external reviewers heard from since the server started.
func (e *Engine) Reviewers() []Reviewer { return e.presence.list() }

// noteReviewer records a poll and, when the reviewer was not present a
// moment ago, resolves the no_reviewer alerts its filter covers. A reviewer
// polling every second does not query the alerts every second.
func (e *Engine) noteReviewer(ctx context.Context, name, repoID string) {
	now := e.now().UTC()
	if e.presence.note(name, repoID, now) {
		return
	}
	open, err := e.store.List(ctx, false)
	if err != nil {
		e.log.Warn("list alerts after a reviewer poll", "err", err)
		return
	}
	for _, a := range open {
		if a.Kind == alert.NoReviewer && (repoID == "" || a.Key == repoID) {
			if err := e.store.Resolve(ctx, a.ID, now); err != nil {
				e.log.Warn("resolve no_reviewer alert", "alert", a.ID, "err", err)
			}
		}
	}
}

// warnNoReviewer raises the repository's no_reviewer alert when nobody who
// could answer has polled within the window. It reports whether the alert
// stands, so the caller logs the hint once per silence.
func (e *Engine) warnNoReviewer(ctx context.Context, repoID, changeID, runID, reviewID, step string) bool {
	now := e.now().UTC()
	if e.presence.available(repoID, now) {
		return false
	}
	a := alert.Alert{Kind: alert.NoReviewer, Key: repoID, RepoID: repoID, CreatedAt: now,
		Message: fmt.Sprintf("Change %s is waiting for an external review of step %s and no reviewer has polled in the last %s. Run plumbix review --watch on a machine with Claude Code, or plumbix review install to keep one running.", changeID, step, presenceWindow),
		Details: map[string]any{"change_id": changeID, "run_id": runID, "review_id": reviewID, "step": step}}
	if _, created, err := e.store.Raise(ctx, a); err != nil {
		e.log.Warn("raise no_reviewer alert", "repo", repoID, "err", err)
	} else if created {
		e.log.Warn("external review waiting with no reviewer", "repo", repoID, "change", changeID, "review", reviewID)
	}
	return true
}
