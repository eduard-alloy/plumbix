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
