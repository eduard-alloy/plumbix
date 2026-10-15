package change_test

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/store"
)

var ctx = context.Background()

func newService(t *testing.T) *change.Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return change.NewService(db)
}

func validRepo() change.RepoInput {
	return change.RepoInput{Host: "github", URL: "https://github.com/eduard-alloy/plumbix"}
}

func validChange(repoID string) change.ChangeInput {
	return change.ChangeInput{
		RepoID: repoID, SourceBranch: "feature/retries", TargetBranch: "main",
		Title: "Add retries", Intent: "Retry flaky uploads",
		AcceptanceCriteria: []change.Criterion{{Text: "Uploads retry 3 times", Check: &change.Check{Steps: []string{"test"}}}, {Text: "Docs updated"}},
	}
}

func registerRepo(t *testing.T, s *change.Service) change.Repo {
	t.Helper()
	r, err := s.RegisterRepo(ctx, validRepo())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// problems returns the field names reported by an *InvalidError.
func problems(t *testing.T, err error) map[string]string {
	t.Helper()
	var inv *change.InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("err = %v, want *InvalidError", err)
	}
	m := map[string]string{}
	for _, p := range inv.Problems {
		m[p.Field] = p.Message
	}
	return m
}

func TestRegisterRepoDefaults(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	if !regexp.MustCompile(`^r_[a-z2-7]{12}$`).MatchString(r.ID) {
		t.Errorf("id = %q", r.ID)
	}
	if len(r.ProtectedBranches) != 1 || r.ProtectedBranches[0] != "main" || r.MergeStrategy != "squash" || r.CreatedAt.IsZero() {
		t.Errorf("defaults = %+v", r)
	}
	list, _ := s.ListRepos(ctx)
	if len(list) != 1 || list[0].ID != r.ID {
		t.Errorf("list = %+v", list)
	}
	if _, err := s.RegisterRepo(ctx, validRepo()); !errors.Is(err, change.ErrConflict) {
		t.Errorf("duplicate: err = %v, want ErrConflict", err)
	}
}

func TestRepoURLRules(t *testing.T) {
	good := []change.RepoInput{
		{Host: "github", URL: "https://github.com/a/b.git"},
		{Host: "azuredevops", URL: "https://dev.azure.com/org/Project%20X/_git/repo"},
		{Host: "azuredevops", URL: "https://myorg.visualstudio.com/proj/_git/repo"},
	}
	for _, in := range good {
		if _, err := newService(t).RegisterRepo(ctx, in); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	bad := []struct {
		in    change.RepoInput
		field string
	}{
		{change.RepoInput{Host: "gitlab", URL: "https://gitlab.com/a/b"}, "host"},
		{change.RepoInput{Host: "github", URL: "http://github.com/a/b"}, "url"},
		{change.RepoInput{Host: "github", URL: "https://dev.azure.com/o/p/_git/r"}, "url"},
		{change.RepoInput{Host: "azuredevops", URL: "https://github.com/a/b"}, "url"},
		{change.RepoInput{Host: "github", URL: "https://github.com/a"}, "url"},
		{change.RepoInput{Host: "github", URL: ""}, "url"},
		{change.RepoInput{Host: "github", URL: "https://github.com/a/b", MergeStrategy: "octopus"}, "merge_strategy"},
		{change.RepoInput{Host: "github", URL: "https://github.com/a/b", ProtectedBranches: []string{"main", "bad..name"}}, "protected_branches[1]"},
		{change.RepoInput{Host: "github", URL: "https://github.com/a/b", ProtectedBranches: []string{"main", "main"}}, "protected_branches[1]"},
	}
	for _, tc := range bad {
		_, err := newService(t).RegisterRepo(ctx, tc.in)
		if _, ok := problems(t, err)[tc.field]; !ok {
			t.Errorf("%+v: problems %v, want one for %s", tc.in, problems(t, err), tc.field)
		}
	}
}

func TestSubmitValidChange(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	before := time.Now().Add(-time.Second)
	c, err := s.Submit(ctx, validChange(r.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^c_[a-z2-7]{12}$`).MatchString(c.ID) || c.State != change.Open {
		t.Errorf("id %q state %s", c.ID, c.State)
	}
	if c.AcceptanceCriteria[0].ID != "AC-1" || c.AcceptanceCriteria[1].ID != "AC-2" {
		t.Errorf("criterion ids = %s, %s", c.AcceptanceCriteria[0].ID, c.AcceptanceCriteria[1].ID)
	}
	if c.Constraints == nil || c.CreatedAt.Before(before) || !c.CreatedAt.Equal(c.UpdatedAt) {
		t.Errorf("constraints %v created %s updated %s", c.Constraints, c.CreatedAt, c.UpdatedAt)
	}
	got, err := s.Get(ctx, c.ID)
	if err != nil || got.ID != c.ID {
		t.Errorf("get = %+v, %v", got, err)
	}
}

func TestCriterionIDs(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	in := validChange(r.ID)
	in.AcceptanceCriteria = []change.Criterion{{Text: "a"}, {ID: "AC-1", Text: "b"}, {Text: "c"}, {ID: "AC-7", Text: "d"}}
	c, err := s.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, cr := range c.AcceptanceCriteria {
		ids = append(ids, cr.ID)
	}
	if strings.Join(ids, ",") != "AC-2,AC-1,AC-3,AC-7" {
		t.Errorf("ids = %v, want AC-2,AC-1,AC-3,AC-7 (missing ids take the smallest unused number)", ids)
	}
}

func TestChangeRules(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	long := strings.Repeat("x", 201)
	cases := []struct {
		name   string
		mutate func(*change.ChangeInput)
		field  string
	}{
		{"missing repo id", func(c *change.ChangeInput) { c.RepoID = "" }, "repo_id"},
		{"missing source", func(c *change.ChangeInput) { c.SourceBranch = "" }, "source_branch"},
		{"bad source", func(c *change.ChangeInput) { c.SourceBranch = "has space" }, "source_branch"},
		{"source dotdot", func(c *change.ChangeInput) { c.SourceBranch = "a..b" }, "source_branch"},
		{"source lock", func(c *change.ChangeInput) { c.SourceBranch = "x.lock" }, "source_branch"},
		{"source leading dash", func(c *change.ChangeInput) { c.SourceBranch = "-x" }, "source_branch"},
		{"source trailing slash", func(c *change.ChangeInput) { c.SourceBranch = "x/" }, "source_branch"},
		{"source at brace", func(c *change.ChangeInput) { c.SourceBranch = "x@{1}" }, "source_branch"},
		{"bad target", func(c *change.ChangeInput) { c.TargetBranch = "ma:in" }, "target_branch"},
		{"same branches", func(c *change.ChangeInput) { c.TargetBranch = c.SourceBranch }, "target_branch"},
		{"missing title", func(c *change.ChangeInput) { c.Title = "  " }, "title"},
		{"long title", func(c *change.ChangeInput) { c.Title = long }, "title"},
		{"missing intent", func(c *change.ChangeInput) { c.Intent = "" }, "intent"},
		{"no criteria", func(c *change.ChangeInput) { c.AcceptanceCriteria = nil }, "acceptance_criteria"},
		{"criterion no text", func(c *change.ChangeInput) { c.AcceptanceCriteria[1].Text = "" }, "acceptance_criteria[1].text"},
		{"criterion bad id", func(c *change.ChangeInput) { c.AcceptanceCriteria[0].ID = "X-1" }, "acceptance_criteria[0].id"},
		{"criterion dup id", func(c *change.ChangeInput) {
			c.AcceptanceCriteria[0].ID, c.AcceptanceCriteria[1].ID = "AC-1", "AC-1"
		}, "acceptance_criteria[1].id"},
		{"check both", func(c *change.ChangeInput) {
			c.AcceptanceCriteria[0].Check = &change.Check{Steps: []string{"test"}, Command: "make"}
		}, "acceptance_criteria[0].check"},
		{"check empty", func(c *change.ChangeInput) { c.AcceptanceCriteria[0].Check = &change.Check{} }, "acceptance_criteria[0].check"},
		{"check bad step", func(c *change.ChangeInput) {
			c.AcceptanceCriteria[0].Check = &change.Check{Steps: []string{"Bad_Step"}}
		}, "acceptance_criteria[0].check.steps[0]"},
		{"empty constraint", func(c *change.ChangeInput) { c.Constraints = []string{"ok", ""} }, "constraints[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validChange(r.ID)
			in.AcceptanceCriteria = append([]change.Criterion(nil), in.AcceptanceCriteria...)
			tc.mutate(&in)
			_, err := s.Submit(ctx, in)
			if _, ok := problems(t, err)[tc.field]; !ok {
				t.Errorf("problems = %v, want one for %s", problems(t, err), tc.field)
			}
		})
	}
}

func TestSubmitListsAllProblems(t *testing.T) {
	s := newService(t)
	_, err := s.Submit(ctx, change.ChangeInput{})
	p := problems(t, err)
	for _, f := range []string{"repo_id", "source_branch", "target_branch", "title", "intent", "acceptance_criteria"} {
		if _, ok := p[f]; !ok {
			t.Errorf("missing problem for %s in %v", f, p)
		}
	}
}

func TestSubmitUnknownRepo(t *testing.T) {
	s := newService(t)
	if _, err := s.Submit(ctx, validChange("r_doesnotexist")); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestOneOpenChangePerBranchAndClose(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	first, err := s.Submit(ctx, validChange(r.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, validChange(r.ID)); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("second open change: err = %v, want ErrConflict", err)
	}
	closed, err := s.Close(ctx, first.ID)
	if err != nil || closed.State != change.Closed || !closed.UpdatedAt.After(first.UpdatedAt) && !closed.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if _, err := s.Close(ctx, first.ID); !errors.Is(err, change.ErrInvalidState) {
		t.Errorf("closing twice: err = %v, want ErrInvalidState", err)
	}
	if _, err := s.Close(ctx, "c_nope"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("closing unknown: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Submit(ctx, validChange(r.ID)); err != nil {
		t.Errorf("after close, new change rejected: %v", err)
	}
}

func TestListFilters(t *testing.T) {
	s := newService(t)
	r := registerRepo(t, s)
	a, _ := s.Submit(ctx, validChange(r.ID))
	in := validChange(r.ID)
	in.SourceBranch = "other"
	b, _ := s.Submit(ctx, in)
	_, _ = s.Close(ctx, a.ID)
	open, err := s.List(ctx, change.Filter{State: change.Open})
	if err != nil || len(open) != 1 || open[0].ID != b.ID {
		t.Errorf("open = %+v, %v", open, err)
	}
	if _, err := s.List(ctx, change.Filter{State: "nonsense"}); problems(t, err)["state"] == "" {
		t.Error("unknown state filter was accepted")
	}
}
