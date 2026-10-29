package change_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestCriteriaSetRules: a set needs a task, an owner and at least one
// criterion, binds steps only, and is numbered by Plumbix; a set a change
// has used is frozen (WU-066, A43).
func TestCriteriaSetRules(t *testing.T) {
	s := newService(t)
	_, err := s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Owner: "eduard@mac", Criteria: []change.Criterion{{Text: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "task") {
		t.Errorf("no task: %v", err)
	}
	_, err = s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/X6Q5JY", Criteria: []change.Criterion{{Text: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Errorf("no owner: %v", err)
	}
	_, err = s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/X6Q5JY", Owner: "eduard@mac"})
	if err == nil || !strings.Contains(err.Error(), "criteria") {
		t.Errorf("no criteria: %v", err)
	}
	_, err = s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/X6Q5JY", Owner: "eduard@mac", Criteria: []change.Criterion{{Text: "x", Check: &change.Check{Command: "rm -rf /"}}}})
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("a command in a set: %v", err)
	}
	// A title is the specification's own, as long as a task title gets (Kai's are); only a criterion's limit bounds it.
	if _, err := s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/LONGTITLE", Title: strings.Repeat("t", 300), Owner: "eduard@mac", Criteria: []change.Criterion{{Text: "x"}}}); err != nil {
		t.Errorf("a long title: %v", err)
	}
	cs, err := s.CreateCriteriaSet(ctx, change.CriteriaSetInput{Task: "kai:plumbix/X6Q5JY", Title: "WU-066", Owner: "eduard@mac",
		Source:   &change.CriteriaSource{Kind: "kai", Project: "plumbix", Ref: "X6Q5JY"},
		Criteria: []change.Criterion{{ID: "AC-9", Text: " The build passes ", Check: &change.Check{Steps: []string{"build", "test"}}}, {Text: "Reads well"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cs.ID, "cs_") || cs.Criteria[0].ID != "AC-1" || cs.Criteria[0].Text != "The build passes" || cs.Criteria[1].ID != "AC-2" || cs.Source == nil || cs.Source.Ref != "X6Q5JY" || len(cs.UsedBy) != 0 {
		t.Fatalf("set = %+v", cs)
	}
	listed, err := s.ListCriteriaSets(ctx, "kai:plumbix/X6Q5JY")
	if err != nil || len(listed) != 1 || listed[0].ID != cs.ID {
		t.Errorf("list by task = %+v, %v", listed, err)
	}
	if other, _ := s.ListCriteriaSets(ctx, "kai:plumbix/other"); len(other) != 0 {
		t.Errorf("list of another task = %+v", other)
	}
	// Editable until a change uses it.
	upd, err := s.UpdateCriteriaSet(ctx, cs.ID, change.CriteriaSetInput{Task: cs.Task, Owner: cs.Owner, Criteria: []change.Criterion{{Text: "Only this now"}}})
	if err != nil || len(upd.Criteria) != 1 || upd.Criteria[0].Text != "Only this now" {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	repo, err := s.RegisterRepo(ctx, change.RepoInput{Host: "github", URL: "https://github.com/a/b"})
	if err != nil {
		t.Fatal(err)
	}
	// A submission naming the set takes its criteria, frozen, and records the set.
	c, err := s.Submit(ctx, change.ChangeInput{RepoID: repo.ID, SourceBranch: "feat", TargetBranch: "main", Title: "t", Intent: "i", CriteriaSet: cs.ID,
		AcceptanceCriteria: []change.Criterion{{Text: "ignored: the set rules"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AcceptanceCriteria) != 1 || c.AcceptanceCriteria[0].Text != "Only this now" || c.AcceptanceCriteria[0].ID != "AC-1" || c.CriteriaSet == nil || c.CriteriaSet.ID != cs.ID || c.CriteriaSet.Owner != "eduard@mac" || c.CriteriaSet.Task != cs.Task {
		t.Fatalf("change = %+v %+v", c.AcceptanceCriteria, c.CriteriaSet)
	}
	if got, _ := s.GetCriteriaSet(ctx, cs.ID); len(got.UsedBy) != 1 || got.UsedBy[0] != c.ID {
		t.Errorf("used_by = %v", got.UsedBy)
	}
	if _, err := s.UpdateCriteriaSet(ctx, cs.ID, change.CriteriaSetInput{Task: cs.Task, Owner: cs.Owner, Criteria: []change.Criterion{{Text: "too late"}}}); !errors.Is(err, change.ErrCriteriaSetUsed) {
		t.Errorf("update of a used set: %v", err)
	}
	// Editing the set later would not touch the change anyway: the copy is the change's own.
	if _, err := s.Submit(ctx, change.ChangeInput{RepoID: repo.ID, SourceBranch: "feat2", TargetBranch: "main", Title: "t", Intent: "i", CriteriaSet: "cs_nosuchset00"}); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("unknown set: %v", err)
	}
	if _, err := s.Submit(ctx, change.ChangeInput{RepoID: repo.ID, SourceBranch: "feat3", TargetBranch: "main", Title: "t", Intent: "i", CriteriaSet: "not-an-id"}); err == nil || !strings.Contains(err.Error(), "criteria_set") {
		t.Errorf("bad set id: %v", err)
	}
	if _, err := s.Submit(ctx, change.ChangeInput{RepoID: repo.ID, SourceBranch: "feat4", TargetBranch: "main", Title: "t", Intent: "i"}); err == nil || !strings.Contains(err.Error(), "acceptance_criteria") {
		t.Errorf("neither a set nor criteria: %v", err)
	}
}
