package pullrequest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/validation"
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
	// The reviewer is the person, the review's text the reason (WU-060).
	if c.State != change.Approved || c.HumanApproval == nil || c.HumanApproval.Reason != "ship it" || c.HumanApproval.Head != "h1" || c.HumanApproval.By != "lead" || c.HumanApproval.Source != change.ApprovalSourceGitHubReview {
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

// TestOpenBindsCriteriaToThePipeline: a pull request's items bind to the
// steps the head's pipeline has; unknown steps and command-looking items
// stay text with a note (WU-062).
func TestOpenBindsCriteriaToThePipeline(t *testing.T) {
	h := newHarness(t)
	h.eng.steps = []string{"build", "test", "lint", "validate"}
	h.gh.prs[1] = pr(1, "t", "## Acceptance criteria\n- Builds (check: build)\n- Typo (check: bulid)\n- Reads well\n- Sneaky (run: curl evil)\n")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c, err := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ac := c.AcceptanceCriteria
	if len(ac) != 4 || ac[0].Check == nil || strings.Join(ac[0].Check.Steps, ",") != "build" || ac[0].Note != "" {
		t.Errorf("bound item = %+v", ac[0])
	}
	if ac[1].Check != nil || !strings.Contains(ac[1].Note, "bulid") || ac[2].Check != nil || ac[2].Note != "" || ac[3].Check != nil || !strings.Contains(ac[3].Note, "command") {
		t.Errorf("criteria = %+v", ac)
	}
	// Without a pipeline file at the head, names are checked for form only and bind.
	h2 := newHarness(t)
	h2.eng.steps = nil
	h2.gh.prs[2] = pr(2, "t", "## Acceptance criteria\n- Builds (check: build)\n")
	if err := h2.s.Sync(ctx, h2.repo); err != nil {
		t.Fatal(err)
	}
	if c2, _ := h2.db.ChangeByPullRequest(ctx, h2.repo.ID, 2); len(c2.AcceptanceCriteria) != 1 || c2.AcceptanceCriteria[0].Check == nil {
		t.Errorf("no pipeline file: %+v", c2.AcceptanceCriteria)
	}
}

// TestEditedPullRequestUpdatesTheChange: an edited title or body reaches
// the change's title, intent and criteria on the next sync, without a new
// run; a merged or closed change is left alone (WU-062).
func TestEditedPullRequestUpdatesTheChange(t *testing.T) {
	h := newHarness(t)
	h.eng.steps = []string{"build", "test"}
	h.gh.prs[1] = pr(1, "First title", "First body\n\n## Acceptance criteria\n- Works\n")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	runs := len(h.eng.validated)
	// The same pull request again changes nothing.
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1); again.UpdatedAt != c.UpdatedAt {
		t.Error("an unchanged pull request rewrote the change")
	}
	edited := pr(1, "Second title", "Second body\n\n## Acceptance criteria\n- Works (check: test)\n- Also builds (check: build)\n")
	h.gh.prs[1] = edited
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	got, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if got.Title != "Second title" || !strings.HasPrefix(got.Intent, "Second body") || len(got.AcceptanceCriteria) != 2 || got.AcceptanceCriteria[1].Check == nil || got.AcceptanceCriteria[1].Check.Steps[0] != "build" || got.AcceptanceCriteria[1].ID != "AC-2" {
		t.Errorf("after the edit: %q %q %+v", got.Title, got.Intent, got.AcceptanceCriteria)
	}
	if len(h.eng.validated) != runs {
		t.Errorf("an edit started a run: %v", h.eng.validated)
	}
	if got.State != c.State {
		t.Errorf("state moved from %s to %s on an edit", c.State, got.State)
	}
	// Closed: the change is final and keeps its text; the still-open pull
	// request gets a new change, as before.
	if _, err := h.svc.Close(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	h.gh.prs[1] = pr(1, "Third title", "x")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if final, _ := h.db.GetChange(ctx, got.ID); final.Title != "Second title" || final.State != change.Closed {
		t.Errorf("a closed change followed an edit: %q %s", final.Title, final.State)
	}
	if fresh, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1); fresh.ID == got.ID || fresh.Title != "Third title" {
		t.Errorf("the open pull request did not get a new change: %+v", fresh)
	}
}

// TestAutoMergeIsRecordedAsTheSystemsAct: GitHub attributes an auto-merge
// to the account that requested it; Plumbix tells the two apart (WU-063).
// A standing auto-merge request by the same login that GitHub names as
// the merger is auto_merge; a request withdrawn before the merge, or a
// merge by someone else, is a person's merge.
func TestAutoMergeIsRecordedAsTheSystemsAct(t *testing.T) {
	cases := []struct {
		name     string
		timeline []host.TimelineEvent
		mergedBy string
		auto     bool
	}{
		{"requested and merged by GitHub", []host.TimelineEvent{{Event: "auto_merge_enabled", Actor: host.Actor{Login: "eduard", Type: "User"}, CreatedAt: t0}}, "eduard", true},
		{"request withdrawn", []host.TimelineEvent{{Event: "auto_merge_enabled", Actor: host.Actor{Login: "eduard", Type: "User"}, CreatedAt: t0}, {Event: "auto_merge_disabled", Actor: host.Actor{Login: "eduard", Type: "User"}, CreatedAt: t0.Add(time.Minute)}}, "eduard", false},
		{"someone else pressed merge", []host.TimelineEvent{{Event: "auto_merge_enabled", Actor: host.Actor{Login: "eduard", Type: "User"}, CreatedAt: t0}}, "lead", false},
		{"no request", nil, "eduard", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.eng.outcome = change.Approved
			h.gh.prs[1] = pr(1, "ok", "")
			if err := h.s.Sync(ctx, h.repo); err != nil {
				t.Fatal(err)
			}
			merged := h.gh.prs[1]
			merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 1
			at := t0.Add(2 * time.Minute)
			merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: tc.mergedBy, Type: "User"}
			h.gh.prs[1] = merged
			h.gh.timeline = map[int][]host.TimelineEvent{1: tc.timeline}
			if err := h.s.Sync(ctx, h.repo); err != nil {
				t.Fatal(err)
			}
			c := h.byPR(t, 1)
			if c.State != change.Merged || c.Merge == nil || c.Merge.AutoMerge != tc.auto || c.Merge.MergedBy != tc.mergedBy {
				t.Errorf("merge = %+v, want auto_merge %v by %s", c.Merge, tc.auto, tc.mergedBy)
			}
			if len(h.eng.merges) != 1 || h.eng.merges[0].AutoMerge != tc.auto {
				t.Errorf("recorded = %+v", h.eng.merges)
			}
		})
	}
	// A timeline that cannot be read leaves the merge a person's, and the merge is still recorded.
	t.Run("timeline unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.eng.outcome = change.Approved
		h.gh.prs[1] = pr(1, "ok", "")
		if err := h.s.Sync(ctx, h.repo); err != nil {
			t.Fatal(err)
		}
		merged := h.gh.prs[1]
		merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 1
		at := t0
		merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: "eduard", Type: "User"}
		h.gh.prs[1] = merged
		h.gh.timelineErr = errors.New("GitHub status 500")
		if err := h.s.Sync(ctx, h.repo); err != nil {
			t.Fatal(err)
		}
		if c := h.byPR(t, 1); c.State != change.Merged || c.Merge == nil || c.Merge.AutoMerge {
			t.Errorf("merge = %+v", c.Merge)
		}
	})
	// The pull request object itself says so when GitHub still carries the request.
	h := newHarness(t)
	h.eng.outcome = change.Approved
	h.gh.prs[1] = pr(1, "ok", "")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	merged := h.gh.prs[1]
	merged.State, merged.Merged, merged.MergeCommitSHA, merged.Commits = "closed", true, "mc1", 1
	at := t0
	merged.MergedAt, merged.MergedBy = &at, &host.Actor{Login: "eduard", Type: "User"}
	merged.AutoMerge = &host.AutoMerge{EnabledBy: host.Actor{Login: "eduard", Type: "User"}}
	h.gh.prs[1] = merged
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if c := h.byPR(t, 1); c.Merge == nil || !c.Merge.AutoMerge {
		t.Errorf("auto_merge from the pull request object: %+v", c.Merge)
	}
	for _, call := range h.gh.calls {
		if call == "timeline 1" {
			t.Error("the timeline was read although the pull request carried the request")
		}
	}
}

// TestPullRequestNamesACriteriaSet: a body line "Acceptance criteria:
// cs_<id>" gives the change the set's criteria, frozen, with the set
// recorded; the body's own list is then not used. An unknown set leaves
// the body's list with the problem recorded (WU-066).
func TestPullRequestNamesACriteriaSet(t *testing.T) {
	h := newHarness(t)
	h.eng.steps = []string{"build", "test"}
	cs, err := h.svc.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/X6Q5JY", Owner: "eduard@mac", Criteria: []change.Criterion{{Text: "From the spec", Check: &change.Check{Steps: []string{"test"}}}, {Text: "Reads well"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.gh.prs[1] = pr(1, "t", "Intro.\n\nAcceptance criteria: "+cs.ID+"\n\n## Acceptance criteria\n- Something the author wrote (check: build)\n")
	h.gh.prs[2] = pr(2, "t", "Acceptance criteria: cs_nosuchset00\n\n## Acceptance criteria\n- The author's own\n")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	c1, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if len(c1.AcceptanceCriteria) != 2 || c1.AcceptanceCriteria[0].Text != "From the spec" || c1.AcceptanceCriteria[0].Check == nil || c1.AcceptanceCriteria[0].ID != "AC-1" || c1.CriteriaSet == nil || c1.CriteriaSet.ID != cs.ID || c1.CriteriaSet.Owner != "eduard@mac" || c1.CriteriaSet.Problem != "" {
		t.Errorf("change from a set = %+v %+v", c1.AcceptanceCriteria, c1.CriteriaSet)
	}
	if got, _ := h.svc.GetCriteriaSet(ctx, cs.ID); len(got.UsedBy) != 1 || got.UsedBy[0] != c1.ID {
		t.Errorf("used_by = %v", got.UsedBy)
	}
	c2, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 2)
	if len(c2.AcceptanceCriteria) != 1 || c2.AcceptanceCriteria[0].Text != "The author's own" || c2.CriteriaSet == nil || c2.CriteriaSet.ID != "cs_nosuchset00" || !strings.Contains(c2.CriteriaSet.Problem, "no criteria set") {
		t.Errorf("change naming an unknown set = %+v %+v", c2.AcceptanceCriteria, c2.CriteriaSet)
	}
	// The gate says where the criteria came from.
	r := finishedRun()
	r.Result.Criteria = []validation.CombinedCriterion{{ID: "AC-1", Text: "From the spec", Status: validation.Met}, {ID: "AC-2", Text: "Reads well", Status: validation.Unknown}}
	g, err := GateCheck(c1, r, t0)
	if err != nil || !strings.Contains(g.Output.Summary, "Criteria from set "+cs.ID+" (kai:plumbix/X6Q5JY, eduard@mac)") {
		t.Errorf("gate for a set:\n%s %v", g.Output.Summary, err)
	}
	r.Result.Criteria = []validation.CombinedCriterion{{ID: "AC-1", Text: "The author's own", Status: validation.Unknown}}
	if g, _ := GateCheck(c2, r, t0); !strings.Contains(g.Output.Summary, "Criteria set cs_nosuchset00: no criteria set") {
		t.Errorf("gate for an unknown set:\n%s", g.Output.Summary)
	}
	// An edit that adds the line to the second pull request takes the set.
	h.gh.prs[2] = pr(2, "t", "Acceptance criteria: "+cs.ID+"\n")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if c2, _ = h.db.ChangeByPullRequest(ctx, h.repo.ID, 2); c2.CriteriaSet == nil || c2.CriteriaSet.ID != cs.ID || c2.CriteriaSet.Problem != "" || len(c2.AcceptanceCriteria) != 2 {
		t.Errorf("after the edit = %+v %+v", c2.AcceptanceCriteria, c2.CriteriaSet)
	}
	// A set's steps are checked against the head's pipeline like a body item's.
	cs2, err := h.svc.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/other", Owner: "eduard@mac", Criteria: []change.Criterion{{Text: "Deploys", Check: &change.Check{Steps: []string{"deploy"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.gh.prs[3] = pr(3, "t", "Acceptance criteria: "+cs2.ID+"\n")
	if err := h.s.Sync(ctx, h.repo); err != nil {
		t.Fatal(err)
	}
	if c3, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 3); len(c3.AcceptanceCriteria) != 1 || c3.AcceptanceCriteria[0].Check != nil || !strings.Contains(c3.AcceptanceCriteria[0].Note, "deploy") || c3.CriteriaSet == nil || c3.CriteriaSet.ID != cs2.ID {
		t.Errorf("a set naming a step the pipeline lacks = %+v %+v", c3.AcceptanceCriteria, c3.CriteriaSet)
	}
	// A pull request cannot write a set: there is no such form in a body.
	if _, ok := SetReference("## Acceptance criteria\n- create set cs_abcdef123456 with: x\n"); ok {
		t.Error("a list item read as a set reference")
	}
}
