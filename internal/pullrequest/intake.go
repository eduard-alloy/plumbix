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
	// PullRequestTimeline lists the pull request's issue timeline, where
	// an auto-merge request and its withdrawal are recorded (WU-063).
	PullRequestTimeline(ctx context.Context, repo change.Repo, number int) ([]host.TimelineEvent, error)
}

// Engine is what intake asks the engine to do.
type Engine interface {
	Validate(ctx context.Context, changeID string) (run.Run, error)
	ApproveByPerson(ctx context.Context, changeID string, who change.Approver, reason string) (change.Change, error)
	RecordExternalMerge(ctx context.Context, changeID string, m engine.ExternalMerge) (change.Change, error)
	// PipelineFile reads the pipeline at a branch head, so a pull request's
	// criteria bind only to steps that exist (WU-062).
	PipelineFile(ctx context.Context, repoID, branch string) (engine.PipelineFileView, error)
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
			return s.follow(ctx, repo, existing, pr)
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

// prText is what a pull request says, as the change carries it.
type prText struct {
	title, intent string
	criteria      []change.Criterion
	// set is the criteria set the body names (WU-066): when found, the
	// change takes the set's criteria and ref records it; when not, ref
	// carries the problem and criteria stays the body's own list.
	set *change.CriteriaSet
	ref *change.CriteriaSetRef
}

// text reads a pull request's title, intent and criteria: the criteria
// from the set the body names (WU-066), else from the body's own list
// bound to the steps of the pipeline at the head (WU-062).
func (s *Syncer) text(ctx context.Context, repo change.Repo, pr host.PullRequest) prText {
	t := prText{intent: strings.TrimSpace(pr.Body)}
	if t.intent == "" {
		t.intent = fmt.Sprintf("%s (pull request #%d has no description)", strings.TrimSpace(pr.Title), pr.Number)
	}
	t.title = truncate(strings.TrimSpace(pr.Title), change.MaxTitle)
	t.intent = truncate(t.intent, change.MaxIntent)
	if id, ok := SetReference(pr.Body); ok {
		cs, err := s.Changes.GetCriteriaSet(ctx, id)
		switch {
		case err == nil:
			t.set, t.ref = &cs, &change.CriteriaSetRef{ID: cs.ID, Owner: cs.Owner, Task: cs.Task, Version: cs.UpdatedAt}
			// The set's steps are checked against the head's pipeline like a
			// body item's (WU-062): one the pipeline lacks stays text with a note.
			t.criteria = unbindUnknown(change.CopyCriteria(cs.Criteria), s.knownStepsOf(ctx, repo, pr))
			return t
		case errors.Is(err, change.ErrNotFound):
			t.ref = &change.CriteriaSetRef{ID: id, Problem: "no criteria set " + id + " on this server; the pull request's own list was used"}
		default:
			t.ref = &change.CriteriaSetRef{ID: id, Problem: "criteria set " + id + " could not be read; the pull request's own list was used"}
			s.Log.Warn("pull request names a criteria set that could not be read", "repo", repo.ID, "pr", pr.Number, "set", id, "err", err)
		}
	}
	t.criteria = ParseCriteria(pr.Body, s.knownSteps(ctx, repo, pr))
	return t
}

// sameRef compares two criteria-set references.
func sameRef(a, b *change.CriteriaSetRef) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || (a.ID == b.ID && a.Owner == b.Owner && a.Task == b.Task && a.Problem == b.Problem)
}

// knownSteps reads the pipeline at the pull request's head and reports
// whether a step exists there. When the file is missing, invalid or the
// read fails, names are checked for form only and the log says so.
func (s *Syncer) knownSteps(ctx context.Context, repo change.Repo, pr host.PullRequest) func(string) bool {
	if !strings.Contains(pr.Body, "(") && !strings.Contains(pr.Body, "（") {
		return nil // nothing to bind; spare the fetch
	}
	return s.knownStepsOf(ctx, repo, pr)
}

// knownStepsOf reads the pipeline at the pull request's head and reports
// whether a step exists there; nil when it cannot be read.
func (s *Syncer) knownStepsOf(ctx context.Context, repo change.Repo, pr host.PullRequest) func(string) bool {
	v, err := s.Engine.PipelineFile(ctx, repo.ID, pr.Head.Branch)
	if err != nil {
		s.Log.Warn("pull request criteria: the pipeline could not be read; step names checked for form only", "repo", repo.ID, "pr", pr.Number, "err", err)
		return nil
	}
	if v.Pipeline == nil {
		return nil
	}
	steps := map[string]bool{}
	for _, st := range v.Pipeline.Steps {
		steps[st.Name] = true
	}
	return func(name string) bool { return steps[name] }
}

// follow brings an open change's title, intent and criteria up to date
// with its pull request after an edit (WU-062). Nothing else moves: no
// run starts, the state stays.
func (s *Syncer) follow(ctx context.Context, repo change.Repo, c change.Change, pr host.PullRequest) error {
	if c.State.Final() {
		return nil
	}
	t := s.text(ctx, repo, pr)
	if t.title == c.Title && t.intent == c.Intent && sameCriteria(t.criteria, c.AcceptanceCriteria) && sameRef(t.ref, c.CriteriaSet) {
		return nil
	}
	if _, err := s.Changes.UpdatePullRequestText(ctx, c.ID, t.title, t.intent, t.criteria, t.ref); err != nil {
		if errors.Is(err, change.ErrInvalidState) {
			return nil
		}
		return fmt.Errorf("follow the edited pull request: %w", err)
	}
	s.Log.Info("pull request edited, change updated", "repo", repo.ID, "pr", pr.Number, "change", c.ID, "criteria", len(t.criteria))
	return nil
}

// sameCriteria compares what the pull request states with what the change
// carries, ids aside (the service assigns them in order).
func sameCriteria(a, b []change.Criterion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Text != y.Text || x.Note != y.Note || (x.Check == nil) != (y.Check == nil) {
			return false
		}
		if x.Check != nil && (strings.Join(x.Check.Steps, ",") != strings.Join(y.Check.Steps, ",") || x.Check.Command != y.Check.Command) {
			return false
		}
	}
	return true
}

// open submits the change a pull request describes and starts its run.
func (s *Syncer) open(ctx context.Context, repo change.Repo, pr host.PullRequest) error {
	t := s.text(ctx, repo, pr)
	in := change.ChangeInput{
		RepoID: repo.ID, SourceBranch: pr.Head.Branch, TargetBranch: pr.Base.Branch,
		Title: t.title, Intent: t.intent,
		AcceptanceCriteria: t.criteria, AllowNoCriteria: true, CriteriaSetRef: t.ref,
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
	m := engine.ExternalMerge{Commit: pr.MergeCommitSHA, HeadSHA: pr.Head.SHA, Commits: pr.Commits, MergedBy: mergedBy,
		AutoMerge: s.autoMerged(ctx, api, repo, pr, mergedBy)}
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

// autoMerged reports whether GitHub merged the pull request on a
// requester's behalf (WU-063): an auto-merge request stood at the merge
// and GitHub names the requester as the merger. GitHub clears the pull
// request's auto_merge object once merged, so the issue timeline is read
// when the object is gone; a request withdrawn before the merge, or a
// merge pressed by someone else, is a person's merge. A timeline that
// cannot be read leaves the merge a person's, and the log says so.
func (s *Syncer) autoMerged(ctx context.Context, api API, repo change.Repo, pr host.PullRequest, mergedBy string) bool {
	requester := ""
	if pr.AutoMerge != nil {
		requester = Submitter(pr.AutoMerge.EnabledBy)
	} else {
		events, err := api.PullRequestTimeline(ctx, repo, pr.Number)
		if err != nil {
			s.Log.Warn("pull request timeline could not be read; the merge is recorded as a person's", "repo", repo.ID, "pr", pr.Number, "err", err)
			return false
		}
		for _, e := range events {
			switch e.Event {
			case "auto_merge_enabled":
				requester = Submitter(e.Actor)
			case "auto_merge_disabled":
				requester = ""
			}
		}
	}
	return requester != "" && requester == mergedBy
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
