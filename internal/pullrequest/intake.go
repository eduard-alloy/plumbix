package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// API is what the package needs from GitHub; *host.GitHub implements it.
type API interface {
	ListPullRequests(ctx context.Context, repo change.Repo, state string) ([]host.PullRequest, error)
	GetPullRequest(ctx context.Context, repo change.Repo, number int) (host.PullRequest, error)
	ListReviews(ctx context.Context, repo change.Repo, number int) ([]host.Review, error)
	CreateCheckRun(ctx context.Context, repo change.Repo, c host.CheckRun) (int64, error)
	UpdateCheckRun(ctx context.Context, repo change.Repo, id int64, c host.CheckRun) error
	RequiredChecks(ctx context.Context, repo change.Repo, branch string) (contexts []string, known bool, err error)
}

// Engine is what intake asks the engine to do.
type Engine interface {
	Validate(ctx context.Context, changeID string) (run.Run, error)
	ApproveByPerson(ctx context.Context, changeID string, who change.Approver, reason string) (change.Change, error)
	RecordExternalMerge(ctx context.Context, changeID string, m engine.ExternalMerge) (change.Change, error)
}

// Store is what intake reads and writes.
type Store interface {
	ListChanges(ctx context.Context, f change.Filter) ([]change.Change, error)
	ChangeByPullRequest(ctx context.Context, repoID string, number int) (change.Change, error)
	ListRuns(ctx context.Context, changeID string) ([]run.Run, error)
	alert.Store
}

// ErrNoAPI means the repository's host has no pull-request API: it is not
// GitHub, or the registry has no GitHub adapter.
var ErrNoAPI = errors.New("the repository's host has no pull-request API")

// Reasons a gate_bypassed alert gives.
const (
	ReasonNotPassed   = "not_passed"   // the change was not approved when GitHub merged
	ReasonNotRequired = "not_required" // plumbix/gate is not a required check on the target
)

// Syncer turns pull requests into changes and back.
type Syncer struct {
	Hosts   host.Source
	Store   Store
	Changes *change.Service
	Engine  Engine
	Log     *slog.Logger
	Now     func() time.Time

	mu    sync.Mutex
	forks map[string]bool // fork pull requests already logged, by repo#number
}

// apiFor returns the pull-request API of the repository's host.
func apiFor(hosts host.Source, repo change.Repo) (API, error) {
	if repo.Host != change.HostGitHub || hosts == nil {
		return nil, ErrNoAPI
	}
	h, err := hosts.Hosts().For(repo)
	if err != nil {
		return nil, err
	}
	api, ok := h.(API)
	if !ok {
		return nil, ErrNoAPI
	}
	return api, nil
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sync brings the repository's changes in line with its pull requests:
// every open pull request has a change, a change whose pull request closed
// is closed or merged, and a change waiting for a person takes a review
// approval. Failures on one pull request do not stop the others.
func (s *Syncer) Sync(ctx context.Context, repo change.Repo) error {
	api, err := apiFor(s.Hosts, repo)
	if err != nil {
		return err
	}
	open, err := api.ListPullRequests(ctx, repo, "open")
	if err != nil {
		return fmt.Errorf("list pull requests: %w", err)
	}
	var errs []error
	seen := map[int]bool{}
	for _, pr := range open {
		seen[pr.Number] = true
		if err := s.Apply(ctx, repo, pr); err != nil {
			errs = append(errs, fmt.Errorf("pull request #%d: %w", pr.Number, err))
		}
	}
	changes, err := s.Store.ListChanges(ctx, change.Filter{RepoID: repo.ID})
	if err != nil {
		return err
	}
	for _, c := range changes {
		if c.State.Final() || c.PullRequest == nil {
			continue
		}
		n := c.PullRequest.Number
		if !seen[n] {
			pr, err := api.GetPullRequest(ctx, repo, n)
			if err != nil {
				errs = append(errs, fmt.Errorf("pull request #%d: %w", n, err))
				continue
			}
			if err := s.Apply(ctx, repo, pr); err != nil {
				errs = append(errs, fmt.Errorf("pull request #%d: %w", n, err))
			}
			continue
		}
		if c.State == change.NeedsHuman || c.State == change.Escalated {
			reviews, err := api.ListReviews(ctx, repo, n)
			if err != nil {
				errs = append(errs, fmt.Errorf("pull request #%d reviews: %w", n, err))
				continue
			}
			if err := s.ApplyReviews(ctx, c, reviews); err != nil {
				errs = append(errs, fmt.Errorf("pull request #%d reviews: %w", n, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Apply acts on one pull request as GitHub describes it: opens its change,
// closes it, or records GitHub's merge.
func (s *Syncer) Apply(ctx context.Context, repo change.Repo, pr host.PullRequest) error {
	if pr.Head.Repo.FullName != "" && pr.Base.Repo.FullName != "" && !strings.EqualFold(pr.Head.Repo.FullName, pr.Base.Repo.FullName) {
		s.logFork(repo, pr)
		return nil
	}
	existing, err := s.Store.ChangeByPullRequest(ctx, repo.ID, pr.Number)
	found := err == nil
	if err != nil && !errors.Is(err, change.ErrNotFound) {
		return err
	}
	switch {
	case pr.State == "open":
		if found {
			return nil
		}
		return s.open(ctx, repo, pr)
	case pr.Merged || pr.MergedAt != nil:
		if !found {
			return nil
		}
		return s.merged(ctx, repo, existing, pr)
	default:
		if !found {
			return nil
		}
		if _, err := s.Changes.Close(ctx, existing.ID); err != nil && !errors.Is(err, change.ErrInvalidState) {
			return err
		}
		s.Log.Info("pull request closed, change closed", "repo", repo.ID, "pr", pr.Number, "change", existing.ID)
		return nil
	}
}

func (s *Syncer) logFork(repo change.Repo, pr host.PullRequest) {
	key := fmt.Sprintf("%s#%d", repo.ID, pr.Number)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forks == nil {
		s.forks = map[string]bool{}
	}
	if !s.forks[key] {
		s.forks[key] = true
		s.Log.Warn("pull request from a fork is not gated: its head is not a branch of the repository", "repo", repo.ID, "pr", pr.Number, "from", pr.Head.Repo.FullName)
	}
}

// open submits the change a pull request describes and starts its run.
func (s *Syncer) open(ctx context.Context, repo change.Repo, pr host.PullRequest) error {
	intent := strings.TrimSpace(pr.Body)
	if intent == "" {
		intent = fmt.Sprintf("%s (pull request #%d has no description)", strings.TrimSpace(pr.Title), pr.Number)
	}
	in := change.ChangeInput{
		RepoID: repo.ID, SourceBranch: pr.Head.Branch, TargetBranch: pr.Base.Branch,
		Title: truncate(strings.TrimSpace(pr.Title), change.MaxTitle), Intent: truncate(intent, change.MaxIntent),
		AcceptanceCriteria: ParseCriteria(pr.Body), AllowNoCriteria: true,
		SubmittedBy: Submitter(pr.Author),
		PullRequest: &change.PullRequestRef{Number: pr.Number, URL: pr.URL},
	}
	c, err := s.Changes.Submit(ctx, in)
	if err != nil {
		if errors.Is(err, change.ErrConflict) {
			s.Log.Warn("pull request not taken: another change is open on its branch", "repo", repo.ID, "pr", pr.Number, "branch", pr.Head.Branch)
			return nil
		}
		return fmt.Errorf("submit: %w", err)
	}
	s.Log.Info("pull request opened as a change", "repo", repo.ID, "pr", pr.Number, "change", c.ID, "by", c.SubmittedBy, "criteria", len(c.AcceptanceCriteria))
	if _, err := s.Engine.Validate(ctx, c.ID); err != nil {
		s.Log.Error("start validation of a pull request", "change", c.ID, "err", err)
	}
	return nil
}

// Submitter names a pull request's author in GitHub's words: the login,
// or app/<slug> for an App.
func Submitter(a host.Actor) string {
	if a.Type == "Bot" {
		return "app/" + strings.TrimSuffix(a.Login, "[bot]")
	}
	return a.Login
}

// merged records GitHub's merge of the change's pull request and raises
// gate_bypassed when the gate had not agreed or was not required.
func (s *Syncer) merged(ctx context.Context, repo change.Repo, c change.Change, pr host.PullRequest) error {
	api, err := apiFor(s.Hosts, repo)
	if err != nil {
		return err
	}
	if pr.MergedBy == nil || pr.Commits == 0 {
		// The list endpoint omits who merged and the commit count.
		full, err := api.GetPullRequest(ctx, repo, pr.Number)
		if err != nil {
			return fmt.Errorf("read the merged pull request: %w", err)
		}
		pr = full
	}
	mergedBy := ""
	if pr.MergedBy != nil {
		mergedBy = Submitter(*pr.MergedBy)
	}
	before := c.State
	m := engine.ExternalMerge{Commit: pr.MergeCommitSHA, HeadSHA: pr.Head.SHA, Commits: pr.Commits, MergedBy: mergedBy}
	if _, err := s.Engine.RecordExternalMerge(ctx, c.ID, m); err != nil {
		return fmt.Errorf("record the merge: %w", err)
	}
	var reasons []string
	if before != change.Approved {
		reasons = append(reasons, ReasonNotPassed)
	}
	contexts, known, err := api.RequiredChecks(ctx, repo, pr.Base.Branch)
	switch {
	case err != nil:
		s.Log.Warn("required checks could not be read", "repo", repo.ID, "branch", pr.Base.Branch, "err", err)
	case known && !contains(contexts, GateName):
		reasons = append(reasons, ReasonNotRequired)
	}
	if len(reasons) == 0 {
		return nil
	}
	s.raiseBypass(ctx, repo, c, pr, before, mergedBy, reasons)
	return nil
}

func (s *Syncer) raiseBypass(ctx context.Context, repo change.Repo, c change.Change, pr host.PullRequest, before change.State, mergedBy string, reasons []string) {
	var why []string
	for _, r := range reasons {
		switch r {
		case ReasonNotPassed:
			why = append(why, fmt.Sprintf("its change was %s, not approved", before))
		case ReasonNotRequired:
			why = append(why, fmt.Sprintf("%s is not a required check on %s, so Plumbix could hold nothing", GateName, pr.Base.Branch))
		}
	}
	msg := fmt.Sprintf("Pull request #%d (%s) was merged on GitHub by %s while %s.", pr.Number, pr.Title, mergedBy, strings.Join(why, " and "))
	a := alert.Alert{Kind: alert.GateBypassed, Key: fmt.Sprintf("%s#%d", repo.ID, pr.Number), RepoID: repo.ID, Message: msg, CreatedAt: s.now().UTC(),
		Details: map[string]any{"number": pr.Number, "url": pr.URL, "merged_by": mergedBy, "commit": pr.MergeCommitSHA, "change_id": c.ID, "state": string(before), "reasons": reasons}}
	if _, created, err := s.Store.Raise(ctx, a); err != nil {
		s.Log.Error("raise gate_bypassed alert", "repo", repo.ID, "pr", pr.Number, "err", err)
	} else if created {
		s.Log.Warn("gate bypassed", "repo", repo.ID, "pr", pr.Number, "msg", msg)
	}
}

// ApplyReviews takes the first approving review on the change's validated
// head by a person who may approve (an owner, member or collaborator, as
// GitHub's author_association says) as the person's approval of the
// change. Anyone can submit a review on a public repository; only those
// count.
func (s *Syncer) ApplyReviews(ctx context.Context, c change.Change, reviews []host.Review) error {
	if c.State != change.NeedsHuman && c.State != change.Escalated {
		return nil
	}
	runs, err := s.Store.ListRuns(ctx, c.ID)
	if err != nil {
		return err
	}
	head := ""
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status == run.Finished && runs[i].Head != "" {
			head = runs[i].Head
			break
		}
	}
	if head == "" {
		return nil
	}
	for _, r := range reviews {
		// The REST API writes the state in upper case, webhook deliveries
		// in lower case.
		if !strings.EqualFold(r.State, "APPROVED") || r.Author.Type != "User" || r.CommitSHA != head {
			continue
		}
		if !r.MayApprove() {
			s.Log.Info("approving review ignored: the reviewer is not an owner, member or collaborator", "change", c.ID, "by", r.Author.Login, "association", r.AuthorAssociation)
			continue
		}
		// The reviewer is the person (WU-060); the review's text is the
		// reason, or a plain sentence when the review has none.
		reason := strings.TrimSpace(r.Body)
		if reason == "" {
			reason = "Approving review on GitHub"
		}
		if utf8.RuneCountInString(reason) > engine.MaxApprovalReason {
			reason = string([]rune(reason)[:engine.MaxApprovalReason])
		}
		if _, err := s.Engine.ApproveByPerson(ctx, c.ID, change.Approver{By: r.Author.Login, Source: change.ApprovalSourceGitHubReview}, reason); err != nil {
			if errors.Is(err, change.ErrInvalidState) {
				return nil
			}
			return err
		}
		s.Log.Info("GitHub review approval taken as a person's approval", "change", c.ID, "by", r.Author.Login, "head", head)
		return nil
	}
	return nil
}

// Revalidate starts a new run for the change of a pull request, as a
// re-requested check asks.
func (s *Syncer) Revalidate(ctx context.Context, repo change.Repo, number int) error {
	c, err := s.Store.ChangeByPullRequest(ctx, repo.ID, number)
	if err != nil {
		return err
	}
	_, err = s.Engine.Validate(ctx, c.ID)
	return err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
