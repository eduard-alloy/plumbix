package change_test

import (
	"errors"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestRepoMergerDefaults: a repository that watches pull requests lets
// GitHub merge unless told otherwise; every other repository keeps Plumbix
// as the merger (WU-054).
func TestRepoMergerDefaults(t *testing.T) {
	s := newService(t)
	plain, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/plain"})
	if err != nil || plain.Merger != change.MergerPlumbix || plain.PullRequests {
		t.Fatalf("plain repo = %+v, %v", plain, err)
	}
	prs, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/prs", PullRequests: true})
	if err != nil || prs.Merger != change.MergerGitHub || !prs.PullRequests {
		t.Fatalf("pull-request repo = %+v, %v", prs, err)
	}
	optIn, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/optin", PullRequests: true, Merger: "plumbix"})
	if err != nil || optIn.Merger != change.MergerPlumbix {
		t.Fatalf("opt-in repo = %+v, %v", optIn, err)
	}
	bad := []struct {
		in    change.RepoInput
		field string
	}{
		{change.RepoInput{Host: "github", URL: "https://github.com/a/b", Merger: "gitlab"}, "merger"},
		{change.RepoInput{Host: "azuredevops", URL: "https://dev.azure.com/o/p/_git/r", PullRequests: true}, "pull_requests"},
		{change.RepoInput{Host: "git", URL: "/srv/app.git", Merger: "github"}, "merger"},
	}
	for _, tc := range bad {
		_, err := newService(t).RegisterRepo(ctx, tc.in)
		if _, ok := problems(t, err)[tc.field]; !ok {
			t.Errorf("%+v: problems %v, want one for %s", tc.in, problems(t, err), tc.field)
		}
	}
}

// TestSubmitKeepsPullRequest: the server-set pull request reference is
// stored with the change.
func TestSubmitKeepsPullRequest(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	in := validChange(r.ID)
	in.PullRequest = &change.PullRequestRef{Number: 12, URL: "https://github.com/a/b/pull/12"}
	c, err := s.Submit(ctx, in)
	if err != nil || c.PullRequest == nil || c.PullRequest.Number != 12 || c.PullRequest.URL != in.PullRequest.URL {
		t.Fatalf("change = %+v, %v", c, err)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got.PullRequest == nil || got.PullRequest.Number != 12 {
		t.Fatalf("read back = %+v, %v", got, err)
	}
}

// TestSetRepoMerging (WU-055): a registered repository's pull_requests and
// merger change under the registration rules; an empty merger takes the
// default for the new pull_requests value.
func TestSetRepoMerging(t *testing.T) {
	s := newService(t)
	r, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/plain"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetRepoMerging(ctx, r.ID, true, "")
	if err != nil || !got.PullRequests || got.Merger != change.MergerGitHub {
		t.Fatalf("pull requests on = %+v, %v", got, err)
	}
	got, err = s.SetRepoMerging(ctx, r.ID, true, change.MergerPlumbix)
	if err != nil || !got.PullRequests || got.Merger != change.MergerPlumbix {
		t.Fatalf("plumbix merges = %+v, %v", got, err)
	}
	if stored, err := s.GetRepo(ctx, r.ID); err != nil || stored.PullRequests != got.PullRequests || stored.Merger != got.Merger || stored.URL != r.URL {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	got, err = s.SetRepoMerging(ctx, r.ID, false, "")
	if err != nil || got.PullRequests || got.Merger != change.MergerPlumbix {
		t.Fatalf("pull requests off = %+v, %v", got, err)
	}
	az, err := s.RegisterRepo(ctx, change.RepoInput{Host: "azuredevops", URL: "https://dev.azure.com/o/p/_git/r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRepoMerging(ctx, az.ID, true, ""); problems(t, err)["pull_requests"] == "" {
		t.Errorf("azure with pull requests: %v", err)
	}
	if _, err := s.SetRepoMerging(ctx, r.ID, false, "gitlab"); problems(t, err)["merger"] == "" {
		t.Errorf("unknown merger: %v", err)
	}
	if _, err := s.SetRepoMerging(ctx, "r_nope", true, ""); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown repo: %v", err)
	}
}

// TestSetRepoMergeStrategy: the strategy of Plumbix's own merges is
// settable after registration under the registration rule (WU-064).
func TestSetRepoMergeStrategy(t *testing.T) {
	s := newService(t)
	r, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/strategy"})
	if err != nil || r.MergeStrategy != "squash" {
		t.Fatalf("registered = %+v, %v", r, err)
	}
	for _, want := range []string{"merge", "rebase", "squash"} {
		got, err := s.SetRepoMergeStrategy(ctx, r.ID, " "+want+" ")
		if err != nil || got.MergeStrategy != want {
			t.Fatalf("%s: %+v, %v", want, got, err)
		}
		if stored, _ := s.GetRepo(ctx, r.ID); stored.MergeStrategy != want || stored.PullRequests || stored.Merger != change.MergerPlumbix {
			t.Errorf("stored after %s = %+v", want, stored)
		}
	}
	if _, err := s.SetRepoMergeStrategy(ctx, r.ID, "octopus"); problems(t, err)["merge_strategy"] == "" {
		t.Errorf("unknown strategy: %v", err)
	}
	if _, err := s.SetRepoMergeStrategy(ctx, "r_nope", "merge"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown repo: %v", err)
	}
}

// TestRepoPausedAndRemoved: a repository can be paused and resumed, and
// removed by a named person once no change of it is open or running;
// removed, it leaves every listing and lookup (WU-065).
func TestRepoPausedAndRemoved(t *testing.T) {
	s := newService(t)
	r, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/retire"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetRepoPaused(ctx, r.ID, true); err != nil || !got.Paused {
		t.Fatalf("pause = %+v, %v", got, err)
	}
	if stored, _ := s.GetRepo(ctx, r.ID); !stored.Paused {
		t.Error("pause not stored")
	}
	if got, err := s.SetRepoPaused(ctx, r.ID, false); err != nil || got.Paused {
		t.Fatalf("resume = %+v, %v", got, err)
	}
	c, err := s.Submit(ctx, change.ChangeInput{RepoID: r.ID, SourceBranch: "feat", TargetBranch: "main", Title: "t", Intent: "i", AcceptanceCriteria: []change.Criterion{{Text: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRepo(ctx, r.ID, "eduard@mac"); !errors.Is(err, change.ErrRepoBusy) {
		t.Errorf("remove with an open change: %v", err)
	}
	if err := s.RemoveRepo(ctx, r.ID, " "); problems(t, err)["by"] == "" {
		t.Errorf("remove without a person: %v", err)
	}
	if _, err := s.Close(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRepo(ctx, r.ID, "eduard@mac"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRepo(ctx, r.ID); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("a removed repository is still found: %v", err)
	}
	if repos, _ := s.ListRepos(ctx); len(repos) != 0 {
		t.Errorf("a removed repository is still listed: %+v", repos)
	}
	// Its changes stay for the record.
	if got, err := s.Get(ctx, c.ID); err != nil || got.State != change.Closed {
		t.Errorf("the change of a removed repository: %+v, %v", got, err)
	}
	if err := s.RemoveRepo(ctx, r.ID, "eduard@mac"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	// The remote can be registered again: the removed row keeps its URL
	// for the record, but only live registrations must be unique.
	again, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/retire"})
	if err != nil || again.ID == r.ID {
		t.Fatalf("register the same remote after a removal: %+v, %v", again, err)
	}
	if _, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/retire"}); !errors.Is(err, change.ErrConflict) {
		t.Errorf("a second live registration of the same remote: %v", err)
	}
}
