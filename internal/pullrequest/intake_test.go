package pullrequest

import (
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
)

// TestSyncOpensChangesForPullRequests: each open pull request becomes one
// change with the pull request's words; a fork's is skipped; a second
// sync adds nothing.
func TestSyncOpensChangesForPullRequests(t *testing.T) {
	h := newHarness(t)
	h.gh.prs[1] = pr(1, "Add retries", "Retry flaky uploads.\n\n## Acceptance criteria\n- Uploads retry three times\n- Docs mention retries\n")
	bot := pr(2, "Bump deps", "")
	bot.Author = host.Actor{Login: "dependabot[bot]", Type: "Bot"}
	h.gh.prs[2] = bot
	fork := pr(3, "From a fork", "x")
	fork.Head.Repo.FullName = "someone/b"
	h.gh.prs[3] = fork

	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c1, err := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c1.SourceBranch != "feat-1" || c1.TargetBranch != "main" || c1.Title != "Add retries" || c1.Intent != "Retry flaky uploads.\n\n## Acceptance criteria\n- Uploads retry three times\n- Docs mention retries" || c1.SubmittedBy != "octocat" || c1.PullRequest == nil || c1.PullRequest.URL != "https://github.com/a/b/pull/1" {
		t.Errorf("change = %+v", c1)
	}
	if len(c1.AcceptanceCriteria) != 2 || c1.AcceptanceCriteria[0].ID != "AC-1" || c1.AcceptanceCriteria[1].Text != "Docs mention retries" {
		t.Errorf("criteria = %+v", c1.AcceptanceCriteria)
	}
	if c1.State != change.NeedsHuman {
		t.Errorf("state = %s: validation did not run", c1.State)
	}
	c2, err := h.db.ChangeByPullRequest(ctx, h.repo.ID, 2)
	if err != nil || c2.SubmittedBy != "app/dependabot" || len(c2.AcceptanceCriteria) != 0 || c2.Intent != "Bump deps (pull request #2 has no description)" {
		t.Errorf("bot change = %+v, %v", c2, err)
	}
	if _, err := h.db.ChangeByPullRequest(ctx, h.repo.ID, 3); err == nil {
		t.Error("a fork's pull request became a change")
	}
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if all, _ := h.db.ListChanges(ctx, change.Filter{RepoID: h.repo.ID}); len(all) != 2 || len(h.eng.validated) != 2 {
		t.Errorf("after a second sync: %d changes, %d validations", len(all), len(h.eng.validated))
	}
}

// TestClosedAndMergedPullRequests: a closed pull request closes its
// change; a merged one is recorded through the engine with GitHub's merge
// details, and an approved change merged with the gate required raises no
// alert.
func TestMergedPullRequestIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.eng.outcome = change.Approved
	h.gh.prs[1] = pr(1, "ok", "")
	h.gh.prs[2] = pr(2, "dropped", "")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	merged := h.gh.prs[1]
	merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 2
	at := t0
	merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: "maintainer", Type: "User"}
	h.gh.prs[1] = merged
	closed := h.gh.prs[2]
	closed.State = "closed"
	h.gh.prs[2] = closed

	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c1, _ := h.db.ListChanges(ctx, change.Filter{RepoID: h.repo.ID})
	byPR := map[int]change.Change{}
	for _, c := range c1 {
		byPR[c.PullRequest.Number] = c
	}
	if byPR[1].State != change.Merged || byPR[1].Merge == nil || byPR[1].Merge.Commit != "mc1" || byPR[1].Merge.MergedBy != "maintainer" {
		t.Errorf("merged change = %s %+v", byPR[1].State, byPR[1].Merge)
	}
	if len(h.eng.merges) != 1 || h.eng.merges[0].HeadSHA != "h1" || h.eng.merges[0].Commits != 2 {
		t.Errorf("merges = %+v", h.eng.merges)
	}
	if byPR[2].State != change.Closed {
		t.Errorf("closed change = %s", byPR[2].State)
	}
	if alerts, _ := h.db.List(ctx, false); len(alerts) != 0 {
		t.Errorf("alerts after a clean merge: %+v", alerts)
	}
}

// TestMergedWithoutApprovalRaisesAlert: GitHub merged while the change
// still needed a person.
func TestMergedWithoutApprovalRaisesAlert(t *testing.T) {
	h := newHarness(t)
	h.gh.prs[1] = pr(1, "rushed", "")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	merged := h.gh.prs[1]
	merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 1
	at := t0
	merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: "maintainer", Type: "User"}
	if err := h.s.Apply(ctx, h.repo, merged); err != nil {
		t.Fatal(err)
	}
	alerts, _ := h.db.List(ctx, false)
	if len(alerts) != 1 || alerts[0].Kind != alert.GateBypassed || alerts[0].Key != h.repo.ID+"#1" || alerts[0].RepoID != h.repo.ID {
		t.Fatalf("alerts = %+v", alerts)
	}
	a := alerts[0]
	reasons, _ := a.Details["reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != ReasonNotPassed || a.Details["merged_by"] != "maintainer" || a.Details["state"] != "needs_human" {
		t.Errorf("details = %+v", a.Details)
	}
	if a.Message != "Pull request #1 (rushed) was merged on GitHub by maintainer while its change was needs_human, not approved." {
		t.Errorf("message = %q", a.Message)
	}
}

// TestMergedWithoutRequiredGateRaisesAlert: the target does not require
// plumbix/gate, so Plumbix could hold nothing; unknown protection raises
// nothing.
func TestMergedWithoutRequiredGateRaisesAlert(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required []string
		known    bool
		want     int
	}{
		{"not required", []string{"ci"}, true, 1},
		{"unknown", nil, false, 0},
		{"required", []string{GateName}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.eng.outcome = change.Approved
			h.gh.required, h.gh.known = tc.required, tc.known
			h.gh.prs[1] = pr(1, "t", "")
			if err := h.s.Sync(ctx, h.repo); err != nil {
				t.Fatal(err)
			}
			merged := h.gh.prs[1]
			merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 1
			at := t0
			merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: "maintainer", Type: "User"}
			if err := h.s.Apply(ctx, h.repo, merged); err != nil {
				t.Fatal(err)
			}
			alerts, _ := h.db.List(ctx, false)
			if len(alerts) != tc.want {
				t.Fatalf("alerts = %+v", alerts)
			}
			if tc.want == 1 && (alerts[0].Details["reasons"].([]any)[0] != ReasonNotRequired || alerts[0].Message != "Pull request #1 (t) was merged on GitHub by maintainer while plumbix/gate is not a required check on main, so Plumbix could hold nothing.") {
				t.Errorf("alert = %+v", alerts[0])
			}
		})
	}
}

// TestReviewApprovalApproves: an approving review by an owner, member or
// collaborator on the validated head is the person's approval; reviews on
// another head, by bots, by anyone else, or asking for changes are not.
func TestReviewApprovalApproves(t *testing.T) {
	h := newHarness(t)
	h.gh.prs[1] = pr(1, "t", "")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	h.gh.reviews[1] = []host.Review{
		{ID: 1, Author: host.Actor{Login: "lead", Type: "User"}, AuthorAssociation: "MEMBER", State: "APPROVED", CommitSHA: "old", Body: "earlier head"},
		{ID: 2, Author: host.Actor{Login: "robot[bot]", Type: "Bot"}, AuthorAssociation: "NONE", State: "APPROVED", CommitSHA: "h1"},
		{ID: 3, Author: host.Actor{Login: "lead", Type: "User"}, AuthorAssociation: "MEMBER", State: "CHANGES_REQUESTED", CommitSHA: "h1", Body: "no"},
		{ID: 5, Author: host.Actor{Login: "passerby", Type: "User"}, AuthorAssociation: "NONE", State: "APPROVED", CommitSHA: "h1", Body: "lgtm"},
		{ID: 6, Author: host.Actor{Login: "contributor", Type: "User"}, AuthorAssociation: "CONTRIBUTOR", State: "APPROVED", CommitSHA: "h1", Body: "lgtm"},
	}
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1); c.State != change.NeedsHuman || len(h.eng.approvals) != 0 {
		t.Fatalf("approved by the wrong review: %s %v", c.State, h.eng.approvals)
	}
	h.gh.reviews[1] = append(h.gh.reviews[1], host.Review{ID: 4, Author: host.Actor{Login: "lead", Type: "User"}, AuthorAssociation: "COLLABORATOR", State: "APPROVED", CommitSHA: "h1", Body: "ship it", SubmittedAt: t0.Add(time.Hour)})
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if c.State != change.Approved || c.HumanApproval == nil || c.HumanApproval.Reason != "Approved on GitHub by lead: ship it" || c.HumanApproval.Head != "h1" {
		t.Fatalf("change = %s %+v", c.State, c.HumanApproval)
	}
	// Once approved, a further sync asks nothing more.
	if err := h.s.Sync(ctx, h.repo); err != nil || len(h.eng.approvals) != 1 {
		t.Errorf("after approval: %v, approvals %v", err, h.eng.approvals)
	}
}

// TestApplySkipsBranchesWithAnotherChange: a branch that already has an
// open change (submitted through the API) is left to it.
func TestApplySkipsBranchesWithAnotherChange(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Submit(ctx, change.ChangeInput{RepoID: h.repo.ID, SourceBranch: "feat-1", TargetBranch: "main", Title: "mine", Intent: "i", AcceptanceCriteria: []change.Criterion{{Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Apply(ctx, h.repo, pr(1, "same branch", "")); err != nil {
		t.Fatal(err)
	}
	if all, _ := h.db.ListChanges(ctx, change.Filter{RepoID: h.repo.ID}); len(all) != 1 || all[0].PullRequest != nil {
		t.Errorf("changes = %+v", all)
	}
}
