package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// Lander is the engine's Landing (WU-059): before Plumbix's own merge
// pushes a target that requires plumbix/gate, it posts the gate as a
// success on the commit about to land, so a ruleset's required check is
// satisfied by the same check run a pull request gets. The commit must
// already be on the host (the engine pushes it to a holding ref first).
type Lander struct {
	Hosts host.Source
	Now   func() time.Time
}

func (l *Lander) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// GateRequired reports whether the branch requires plumbix/gate. A host
// without a pull-request API, or one that would not say, is unknown, and
// the merge then pushes as it always did.
func (l *Lander) GateRequired(ctx context.Context, repo change.Repo, branch string) (required, known bool, err error) {
	api, err := apiFor(l.Hosts, repo)
	if errors.Is(err, ErrNoAPI) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	contexts, known, err := api.RequiredChecks(ctx, repo, branch)
	if err != nil {
		return false, false, err
	}
	if !known {
		return false, false, nil
	}
	for _, c := range contexts {
		if c == GateName {
			return true, true, nil
		}
	}
	return false, true, nil
}

// PostGate posts the gate on the landing commit: the run's result embedded
// as on a pull request, the title saying Plumbix landed the validated
// tree, the summary naming the validated head the run judged.
func (l *Lander) PostGate(ctx context.Context, repo change.Repo, c change.Change, r run.Run, sha string) error {
	api, err := apiFor(l.Hosts, repo)
	if err != nil {
		return fmt.Errorf("post the gate on %s: %w", repo.ID, err)
	}
	gate, err := GateCheck(c, r, l.now().UTC())
	if err != nil {
		return err
	}
	gate.HeadSHA = sha
	gate.Conclusion = "success"
	// The merge happens only for an approved change, by the validators or
	// by a person after a held run; the title says which, not the held
	// run's own word.
	approval := "approved by the validators"
	if r.Result == nil || r.Result.State != change.Approved {
		approval = "approved by a person"
		if a := c.HumanApproval; a != nil && a.Reason != "" {
			approval += " (" + a.Reason + ")"
		}
	}
	gate.Output.Title = "Landed by Plumbix: " + approval
	gate.Output.Summary = fmt.Sprintf("Plumbix lands the tree validated at %s (run %s, iteration %d), %s, on %s as this commit.\n\n%s", r.Head, r.ID, r.Iteration, approval, c.TargetBranch, gate.Output.Summary)
	_, err = api.CreateCheckRun(ctx, repo, gate)
	return err
}
