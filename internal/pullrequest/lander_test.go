package pullrequest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
)

// TestLanderPostsTheGateOnTheLandingCommit: the Lander says whether the
// target requires plumbix/gate, and posts the gate as a success on the
// commit Plumbix is about to land, with the run's result embedded as on a
// pull request and the validated head named (WU-059). Without an API the
// requirement is unknown and nothing is posted.
func TestLanderPostsTheGateOnTheLandingCommit(t *testing.T) {
	h := newHarness(t)
	l := &Lander{Hosts: host.Registry{change.HostGitHub: h.gh}, Now: func() time.Time { return t0 }}
	if required, known, err := l.GateRequired(ctx, h.repo, "main"); err != nil || !required || !known {
		t.Errorf("required by the fake's rules: %v %v %v", required, known, err)
	}
	h.gh.required = []string{"ci/build"}
	if required, known, err := l.GateRequired(ctx, h.repo, "main"); err != nil || required || !known {
		t.Errorf("another check required: %v %v %v", required, known, err)
	}
	h.gh.known = false
	if required, known, err := l.GateRequired(ctx, h.repo, "main"); err != nil || required || known {
		t.Errorf("unknown: %v %v %v", required, known, err)
	}
	plain := change.Repo{ID: "r_git", Host: change.HostGit, URL: "/tmp/x.git"}
	if required, known, err := l.GateRequired(ctx, plain, "main"); err != nil || required || known {
		t.Errorf("a host without an API: %v %v %v", required, known, err)
	}

	c, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "feat", TargetBranch: "main", Title: "t", Intent: "i", AcceptanceCriteria: []change.Criterion{{Text: "Tests pass"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := finishedRun()
	r.ChangeID, r.RepoID, r.Head = c.ID, h.repo.ID, "validated1"
	r.Outcome = change.Approved
	r.Result.State = change.Approved
	if err := l.PostGate(ctx, h.repo, c, r, "landing1"); err != nil {
		t.Fatal(err)
	}
	gates := h.gh.checks(GateName)
	if len(gates) != 1 {
		t.Fatalf("gates = %+v", gates)
	}
	g := gates[0]
	if g.HeadSHA != "landing1" || g.Status != "completed" || g.Conclusion != "success" || g.ExternalID != r.ID || g.Output == nil {
		t.Fatalf("gate = %+v", g)
	}
	if g.Output.Title != "Landed by Plumbix: approved by the validators" || !strings.Contains(g.Output.Summary, "validated1") || !strings.Contains(g.Output.Summary, r.ID) {
		t.Errorf("title %q summary %q", g.Output.Title, g.Output.Summary)
	}
	// A change a person approved after a held run lands with that said in
	// the title, never the held run's own word beside a green check.
	held := r
	held.Outcome = change.NeedsHuman
	held.Result.State = change.NeedsHuman
	c.HumanApproval = &change.HumanApproval{Head: "validated1", Reason: "reviewed by hand"}
	if err := l.PostGate(ctx, h.repo, c, held, "landing2"); err != nil {
		t.Fatal(err)
	}
	gates = h.gh.checks(GateName)
	// The reason is free text, so it goes in the summary, never the title.
	if len(gates) != 2 || gates[1].Conclusion != "success" || gates[1].Output.Title != "Landed by Plumbix: approved by a person" || !strings.Contains(gates[1].Output.Summary, "Reason: reviewed by hand") {
		t.Errorf("held then approved: %+v", gates[len(gates)-1].Output)
	}
	if marker, _, ok := Extract(g.Output.Text); !ok || marker != ResultMarker {
		t.Errorf("the run's result is not embedded: %q", g.Output.Text)
	}
	if err := l.PostGate(ctx, plain, c, r, "landing1"); err == nil {
		t.Error("posting without an API must fail, not pass silently")
	}
}

// TestLanderClosesTheMergedPullRequest: after Plumbix's own merge the
// Lander comments on the pull request with the commit that landed and
// closes it, as the App; a pull request GitHub already shows merged or
// closed gets the comment only; a host without an API is left alone
// (WU-056).
func TestLanderClosesTheMergedPullRequest(t *testing.T) {
	h := newHarness(t)
	l := &Lander{Hosts: host.Registry{change.HostGitHub: h.gh}, Now: func() time.Time { return t0 }}
	h.gh.prs[7] = pr(7, "Add retries", "")
	c, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "feat-7", TargetBranch: "main", Title: "t", Intent: "i", AllowNoCriteria: true, PullRequest: &change.PullRequestRef{Number: 7, URL: "https://github.com/a/b/pull/7"}})
	if err != nil {
		t.Fatal(err)
	}
	m := change.Merge{Commit: "landed1", Previous: "m1", Strategy: "squash", RunID: "run_000000000001", MergedAt: t0}
	if err := l.PullRequestLanded(ctx, h.repo, c, m); err != nil {
		t.Fatal(err)
	}
	if got := h.gh.prs[7].State; got != "closed" {
		t.Errorf("state = %s", got)
	}
	comments := h.gh.comments[7]
	if len(comments) != 1 || !strings.Contains(comments[0], "landed1") || !strings.Contains(comments[0], "main") || !strings.Contains(comments[0], c.ID) || !strings.Contains(comments[0], "squash") {
		t.Errorf("comments = %q", comments)
	}

	// GitHub recognised a merge commit or a rebase as the pull request's
	// merge before Plumbix got here: the comment names the commit, no
	// close call is made.
	done := pr(8, "Rebased", "")
	done.State, done.Merged = "closed", true
	h.gh.prs[8] = done
	c8 := c
	c8.PullRequest = &change.PullRequestRef{Number: 8}
	h.gh.calls = nil
	if err := l.PullRequestLanded(ctx, h.repo, c8, change.Merge{Commit: "h1", Strategy: "rebase"}); err != nil {
		t.Fatal(err)
	}
	if len(h.gh.comments[8]) != 1 || strings.Join(h.gh.calls, ",") != "get 8,comment 8" {
		t.Errorf("already merged: comments %q calls %v", h.gh.comments[8], h.gh.calls)
	}

	// A comment that fails once the pull request is closed is lost and
	// logged, not an error: the sync would otherwise retry a close that
	// has nothing left to close.
	h.gh.prs[9] = pr(9, "Comment lost", "")
	c9 := c
	c9.PullRequest = &change.PullRequestRef{Number: 9}
	h.gh.commentErr = errors.New("GitHub status 403")
	if err := l.PullRequestLanded(ctx, h.repo, c9, m); err != nil || h.gh.prs[9].State != "closed" {
		t.Errorf("comment lost: err %v state %s", err, h.gh.prs[9].State)
	}
	h.gh.commentErr = nil

	plain := change.Repo{ID: "r_git", Host: change.HostGit, URL: "/tmp/x.git"}
	if err := l.PullRequestLanded(ctx, plain, c, m); err != nil {
		t.Errorf("a host without an API: %v", err)
	}
	// A change without a pull request has nothing to close.
	noPR := c
	noPR.PullRequest = nil
	h.gh.calls = nil
	if err := l.PullRequestLanded(ctx, h.repo, noPR, m); err != nil || len(h.gh.calls) != 0 {
		t.Errorf("no pull request: %v %v", err, h.gh.calls)
	}
}
