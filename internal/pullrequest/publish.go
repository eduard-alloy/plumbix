package pullrequest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// PublishStore is what the publisher reads.
type PublishStore interface {
	GetRepo(ctx context.Context, id string) (change.Repo, error)
	GetChange(ctx context.Context, id string) (change.Change, error)
	GetRun(ctx context.Context, id string) (run.Run, error)
}

// Publisher posts check runs for pull-request changes as events arrive:
// the gate in progress when a run starts, one check per validator and the
// gate's conclusion when it ends, and the gate's success when a person
// approves.
type Publisher struct {
	Hosts host.Source
	Store PublishStore
	Log   *slog.Logger
	Now   func() time.Time

	mu    sync.Mutex
	gates map[string]int64 // check-run id of the running gate, by run id
	order []string         // run ids in insertion order, to bound gates
}

// maxGates bounds the running gates remembered: a run whose run.finished
// was missed (a lagged subscription) would otherwise stay forever.
const maxGates = 256

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Run handles events from the hub until ctx ends. A lagged subscription
// is replaced; the check runs of the events missed meanwhile are not
// posted, which the log says.
func (p *Publisher) Run(ctx context.Context, hub *events.Hub) {
	for ctx.Err() == nil {
		sub := hub.Subscribe(events.Filter{})
		p.drain(ctx, sub)
		sub.Close()
		if !sub.Lagged() {
			return
		}
		p.Log.Warn("check-run publisher fell behind the event stream; some check runs were not posted")
	}
}

func (p *Publisher) drain(ctx context.Context, sub *events.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if err := p.Handle(ctx, e); err != nil {
				p.Log.Error("post check run", "event", e.Type, "change", e.ChangeID, "run", e.RunID, "err", err)
			}
		}
	}
}

// Handle posts what one event calls for.
func (p *Publisher) Handle(ctx context.Context, e events.Event) error {
	if e.ChangeID == "" {
		return nil
	}
	switch e.Type {
	case events.RunStarted, events.StepStarted:
		return p.gateRunning(ctx, e)
	case events.RunFinished:
		return p.runFinished(ctx, e)
	case events.ApprovedByPerson:
		return p.approved(ctx, e)
	}
	return nil
}

// target resolves the event's change, repository and API when the change
// mirrors a pull request on a repository Plumbix gates.
func (p *Publisher) target(ctx context.Context, changeID string) (change.Change, change.Repo, API, bool, error) {
	c, err := p.Store.GetChange(ctx, changeID)
	if err != nil {
		return c, change.Repo{}, nil, false, err
	}
	if c.PullRequest == nil {
		return c, change.Repo{}, nil, false, nil
	}
	repo, err := p.Store.GetRepo(ctx, c.RepoID)
	if err != nil {
		return c, repo, nil, false, err
	}
	if !repo.PullRequests {
		return c, repo, nil, false, nil
	}
	api, err := apiFor(p.Hosts, repo)
	if err != nil {
		return c, repo, nil, false, err
	}
	return c, repo, api, true, nil
}

func (p *Publisher) gateRunning(ctx context.Context, e events.Event) error {
	p.mu.Lock()
	_, posted := p.gates[e.RunID]
	p.mu.Unlock()
	if posted {
		return nil
	}
	r, err := p.Store.GetRun(ctx, e.RunID)
	if err != nil {
		return err
	}
	if r.Head == "" || r.Status != run.Running {
		// Before the fetch there is no commit to attach a check to; the
		// first step.started comes after it.
		return nil
	}
	_, repo, api, ok, err := p.target(ctx, e.ChangeID)
	if err != nil || !ok {
		return err
	}
	id, err := api.CreateCheckRun(ctx, repo, GateRunning(r))
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.gates == nil {
		p.gates = map[string]int64{}
	}
	p.gates[e.RunID] = id
	p.order = append(p.order, e.RunID)
	for len(p.order) > maxGates {
		delete(p.gates, p.order[0])
		p.order = p.order[1:]
	}
	p.mu.Unlock()
	return nil
}

func (p *Publisher) runFinished(ctx context.Context, e events.Event) error {
	c, repo, api, ok, err := p.target(ctx, e.ChangeID)
	if err != nil || !ok {
		return err
	}
	r, err := p.Store.GetRun(ctx, e.RunID)
	if err != nil {
		return err
	}
	if r.Status == run.Cancelled {
		return p.finishGate(ctx, api, repo, r.ID, host.CheckRun{Name: GateName, HeadSHA: r.Head, Status: "completed", Conclusion: "cancelled", ExternalID: r.ID,
			Output: &host.CheckOutput{Title: "Cancelled", Summary: "Run " + r.ID + " was cancelled: " + r.Error}})
	}
	if r.Head == "" {
		p.Log.Warn("run ended before fetching; no commit to post check runs on", "run", r.ID, "change", c.ID, "err", r.Error)
		return nil
	}
	now := p.now().UTC()
	if r.Result != nil {
		for _, v := range r.Result.Validators {
			check, err := ValidatorCheck(r, v, now)
			if err != nil {
				return err
			}
			if _, err := api.CreateCheckRun(ctx, repo, check); err != nil {
				return fmt.Errorf("check run %s: %w", check.Name, err)
			}
		}
	}
	gate, err := GateCheck(c, r, now)
	if err != nil {
		return err
	}
	return p.finishGate(ctx, api, repo, r.ID, gate)
}

// finishGate completes the running gate of a run, or creates one when the
// running gate was never posted (a restart in between).
func (p *Publisher) finishGate(ctx context.Context, api API, repo change.Repo, runID string, gate host.CheckRun) error {
	p.mu.Lock()
	id, ok := p.gates[runID]
	delete(p.gates, runID)
	p.mu.Unlock()
	if ok {
		if err := api.UpdateCheckRun(ctx, repo, id, gate); err == nil {
			return nil
		} else {
			p.Log.Warn("update the running gate check; creating a new one", "run", runID, "err", err)
		}
	}
	_, err := api.CreateCheckRun(ctx, repo, gate)
	return err
}

func (p *Publisher) approved(ctx context.Context, e events.Event) error {
	c, repo, api, ok, err := p.target(ctx, e.ChangeID)
	if err != nil || !ok {
		return err
	}
	gate, ok := GateApproved(c, p.now().UTC())
	if !ok {
		return nil
	}
	_, err = api.CreateCheckRun(ctx, repo, gate)
	return err
}
