package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Log   *slog.Logger // nil discards
}

func (l *Lander) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.DiscardHandler)
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
	approval, reason := "approved by the validators", ""
	if r.Result == nil || r.Result.State != change.Approved {
		approval = "approved by a person"
		if a := c.HumanApproval; a != nil && a.Reason != "" {
			reason = "\n\nReason: " + a.Reason
		}
	}
	gate.Output.Title = "Landed by Plumbix: " + approval
	gate.Output.Summary = clip(fmt.Sprintf("Plumbix lands the tree validated at %s (run %s, iteration %d), %s, on %s as this commit.%s\n\n%s", r.Head, r.ID, r.Iteration, approval, c.TargetBranch, reason, gate.Output.Summary), maxSummary)
	_, err = api.CreateCheckRun(ctx, repo, gate)
	return err
}

// PullRequestLanded finishes a pull request whose change Plumbix merged
// itself (WU-056): a comment names the commit that landed, and the pull
// request is closed when GitHub still shows it open, since GitHub matches
// a merge commit or a rebase to the pull request on its own but never a
// squash. Both calls go out as the App. A change without a pull request,
// or a host without an API, is nothing to do.
func (l *Lander) PullRequestLanded(ctx context.Context, repo change.Repo, c change.Change, m change.Merge) error {
	if c.PullRequest == nil {
		return nil
	}
	api, err := apiFor(l.Hosts, repo)
	if errors.Is(err, ErrNoAPI) {
		return nil
	}
	if err != nil {
		return err
	}
	comment := fmt.Sprintf("Plumbix merged this pull request: `%s` is now the head of `%s` (%s of the validated tree; change `%s`, run `%s`).", short(m.Commit), c.TargetBranch, m.Strategy, c.ID, m.RunID)
	return closePullRequest(ctx, l.log(), api, repo, c, c.PullRequest.Number, comment)
}

// closePullRequest closes the pull request unless GitHub already shows it
// merged or closed, then posts the comment, if any. The close comes
// first: the sync retries a failed close on every pass, and a comment
// posted before it would be repeated each time. An error means the pull
// request is still open and worth another try; a comment that fails once
// the pull request is closed is lost, and logged here as that, since a
// closed pull request is not visited again.
func closePullRequest(ctx context.Context, log *slog.Logger, api API, repo change.Repo, c change.Change, number int, comment string) error {
	pr, err := api.GetPullRequest(ctx, repo, number)
	if err != nil {
		return fmt.Errorf("read pull request #%d: %w", number, err)
	}
	if pr.State == "open" {
		if err := api.ClosePullRequest(ctx, repo, number); err != nil {
			return fmt.Errorf("close pull request #%d: %w", number, err)
		}
	}
	if comment == "" {
		return nil
	}
	if err := api.CommentOnPullRequest(ctx, repo, number, comment); err != nil {
		log.Warn("pull request closed, but the comment naming the merge was lost", "repo", repo.ID, "pr", number, "change", c.ID, "err", err)
	}
	return nil
}
