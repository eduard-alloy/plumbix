package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// MaxApprovalReason bounds the reason a person gives for an approval.
const MaxApprovalReason = 1000

// ApproveByPerson records that a person approved a needs_human or escalated
// change at its latest validated head, with a reason, and moves it to
// approved so the normal merge applies. A later push starts a new run,
// which ends the approval.
func (e *Engine) ApproveByPerson(ctx context.Context, changeID string, who change.Approver, reason string) (change.Change, error) {
	reason = strings.TrimSpace(reason)
	who.By = strings.TrimSpace(who.By)
	if who.Source == "" {
		who.Source = change.ApprovalSourcePlumbix
	}
	var problems []change.Problem
	switch {
	case reason == "":
		problems = append(problems, change.Problem{Field: "reason", Message: "is required: say why the change is fine despite the verdict"})
	case utf8.RuneCountInString(reason) > MaxApprovalReason:
		problems = append(problems, change.Problem{Field: "reason", Message: fmt.Sprintf("is longer than %d characters", MaxApprovalReason)})
	}
	switch {
	case who.By == "":
		problems = append(problems, change.Problem{Field: "by", Message: "is required: name the person approving"})
	case utf8.RuneCountInString(who.By) > change.MaxSubmittedBy:
		problems = append(problems, change.Problem{Field: "by", Message: fmt.Sprintf("is longer than %d characters", change.MaxSubmittedBy)})
	}
	if who.Source != change.ApprovalSourcePlumbix && who.Source != change.ApprovalSourceGitHubReview {
		problems = append(problems, change.Problem{Field: "source", Message: "must be plumbix or github_review"})
	}
	if len(problems) > 0 {
		return change.Change{}, &change.InvalidError{Problems: problems}
	}
	c, err := e.store.GetChange(ctx, changeID)
	if err != nil {
		return change.Change{}, err
	}
	if c.State != change.NeedsHuman && c.State != change.Escalated {
		return change.Change{}, fmt.Errorf("%w: change %s is %s; a person approves needs_human or escalated changes", change.ErrInvalidState, changeID, c.State)
	}
	runs, err := e.store.ListRuns(ctx, changeID)
	if err != nil {
		return change.Change{}, err
	}
	// The same run the merge will land: the latest one, and it must have
	// finished with a judged head. An errored or cancelled last run leaves
	// nothing to approve.
	if len(runs) == 0 {
		return change.Change{}, fmt.Errorf("%w: change %s has no finished run to approve", change.ErrInvalidState, changeID)
	}
	last := &runs[len(runs)-1]
	if last.Status != run.Finished || last.Head == "" {
		return change.Change{}, fmt.Errorf("%w: change %s has no finished run to approve (the latest run %s)", change.ErrInvalidState, changeID, last.Status)
	}
	if err := e.store.ApproveByPerson(ctx, changeID, c.State, change.HumanApproval{Reason: reason, Head: last.Head, At: e.now().UTC(), By: who.By, Source: who.Source}); err != nil {
		return change.Change{}, err
	}
	e.log.Info("approved by a person", "change", changeID, "head", short(last.Head), "by", who.By, "source", who.Source)
	return e.store.GetChange(ctx, changeID)
}
