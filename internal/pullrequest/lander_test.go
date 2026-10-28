package pullrequest

import (
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
	if !strings.Contains(g.Output.Title, "Landed by Plumbix") || !strings.Contains(g.Output.Summary, "validated1") || !strings.Contains(g.Output.Summary, r.ID) {
		t.Errorf("title %q summary %q", g.Output.Title, g.Output.Summary)
	}
	if marker, _, ok := Extract(g.Output.Text); !ok || marker != ResultMarker {
		t.Errorf("the run's result is not embedded: %q", g.Output.Text)
	}
	if err := l.PostGate(ctx, plain, c, r, "landing1"); err == nil {
		t.Error("posting without an API must fail, not pass silently")
	}
}
