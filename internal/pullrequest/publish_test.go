package pullrequest

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// TestPublisherPostsCheckRuns: the gate goes in_progress once the run has a
// commit, every validator gets a check run when the run ends, the gate is
// completed from the running one, and a person's approval turns it green.
func TestPublisherPostsCheckRuns(t *testing.T) {
	h := newHarness(t)
	p := &Publisher{Hosts: host.Registry{change.HostGitHub: h.gh}, Store: h.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return t0 }}
	c, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "feat", TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{Text: "Tests pass"}}, PullRequest: &change.PullRequestRef{Number: 1, URL: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	r := finishedRun()
	r.ChangeID, r.RepoID = c.ID, h.repo.ID
	if err := h.db.CreateRun(ctx, run.Run{ID: r.ID, RepoID: r.RepoID, ChangeID: c.ID, Branch: "feat", Iteration: 2, Status: run.Running, StartedAt: t0, Steps: []run.Step{}}); err != nil {
		t.Fatal(err)
	}
	started := events.Event{Type: events.RunStarted, RepoID: h.repo.ID, ChangeID: c.ID, RunID: r.ID}
	if err := p.Handle(ctx, started); err != nil {
		t.Fatal(err)
	}
	if n := len(h.gh.created); n != 0 {
		t.Fatalf("gate posted before the run had a commit: %+v", h.gh.created)
	}
	if err := h.db.SetRunCommits(ctx, r.ID, "h1", "b1"); err != nil {
		t.Fatal(err)
	}
	step := events.Event{Type: events.StepStarted, RepoID: h.repo.ID, ChangeID: c.ID, RunID: r.ID}
	if err := p.Handle(ctx, step); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, step); err != nil {
		t.Fatal(err)
	}
	gates := h.gh.checks(GateName)
	if len(gates) != 1 || gates[0].Status != "in_progress" || gates[0].HeadSHA != "h1" {
		t.Fatalf("running gate = %+v", gates)
	}

	if err := h.db.FinishRun(ctx, r.ID, run.Finish{Status: run.Finished, Outcome: r.Outcome, Result: r.Result, Steps: []run.Step{}, At: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, events.Event{Type: events.RunFinished, RepoID: h.repo.ID, ChangeID: c.ID, RunID: r.ID}); err != nil {
		t.Fatal(err)
	}
	if ref := h.gh.checks("plumbix/ref"); len(ref) != 1 || ref[0].Conclusion != "failure" || ref[0].HeadSHA != "h1" {
		t.Errorf("ref check = %+v", ref)
	}
	if adv := h.gh.checks("plumbix/second"); len(adv) != 1 || adv[0].Conclusion != "neutral" {
		t.Errorf("advisory check = %+v", adv)
	}
	if len(h.gh.checks(GateName)) != 1 {
		t.Errorf("a second gate was created instead of completing the running one: %+v", h.gh.created)
	}
	done, ok := h.gh.updated[1]
	if !ok || done.Status != "completed" || done.Conclusion != "failure" || done.Output.Title != "Fixes requested" {
		t.Errorf("completed gate = %+v (%v)", done, ok)
	}
	if _, _, ok := Extract(done.Output.Text); !ok {
		t.Error("completed gate carries no embedded result")
	}

	// A person approves on Plumbix: the gate turns green on that head.
	if err := h.db.SetChangeState(ctx, c.ID, change.Open, change.NeedsHuman, t0); err != nil {
		t.Fatal(err)
	}
	if err := h.db.ApproveByPerson(ctx, c.ID, change.NeedsHuman, change.HumanApproval{Reason: "known flake", Head: "h1", At: t0, By: "tester", Source: change.ApprovalSourcePlumbix}); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, events.Event{Type: events.ApprovedByPerson, RepoID: h.repo.ID, ChangeID: c.ID}); err != nil {
		t.Fatal(err)
	}
	gates = h.gh.checks(GateName)
	if len(gates) != 2 || gates[1].Conclusion != "success" || gates[1].HeadSHA != "h1" || !strings.Contains(gates[1].Output.Summary, "known flake") {
		t.Errorf("approved gate = %+v", gates)
	}

	// A change without a pull request, or a repository without the
	// setting, gets no check runs.
	plain, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "other", TargetBranch: "main", Title: "t", Intent: "i", AcceptanceCriteria: []change.Criterion{{Text: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(h.gh.created)
	if err := p.Handle(ctx, events.Event{Type: events.RunFinished, RepoID: h.repo.ID, ChangeID: plain.ID, RunID: r.ID}); err != nil || len(h.gh.created) != before {
		t.Errorf("plain change: %v, %d check runs", err, len(h.gh.created)-before)
	}
}

// TestPublisherFollowsTheHub: events published on the hub reach the
// publisher in order.
func TestPublisherFollowsTheHub(t *testing.T) {
	h := newHarness(t)
	hub := events.NewHub(h.db)
	h.db.OnEvent(hub.Publish)
	defer hub.Close()
	p := &Publisher{Hosts: host.Registry{change.HostGitHub: h.gh}, Store: h.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	stop := make(chan struct{})
	go func() { p.Run(ctx, hub); close(stop) }()
	c, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "feat", TargetBranch: "main", Title: "t", Intent: "i",
		AcceptanceCriteria: []change.Criterion{{Text: "x"}}, PullRequest: &change.PullRequestRef{Number: 1, URL: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	h.eng.outcome = change.Approved
	if _, err := h.eng.Validate(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	// The fake engine finishes the run at once, so the running gate may or
	// may not have been posted before run.finished; either way the gate
	// ends green, created or updated.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.gh.mu.Lock()
		var final []host.CheckRun
		for _, c := range h.gh.created {
			if c.Name == GateName && c.Status == "completed" {
				final = append(final, c)
			}
		}
		for _, c := range h.gh.updated {
			if c.Name == GateName && c.Status == "completed" {
				final = append(final, c)
			}
		}
		h.gh.mu.Unlock()
		if len(final) == 1 {
			if final[0].Conclusion != "success" {
				t.Fatalf("gate = %+v", final)
			}
			hub.Close()
			<-stop
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no gate posted through the hub: %+v", h.gh.created)
}
