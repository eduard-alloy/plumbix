package change

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrInvalidState means the requested transition is not allowed from the
// change's current state.
var ErrInvalidState = errors.New("invalid state")

// idChars is the number of random characters in an ID after its prefix.
const idChars = 12

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Service applies the change rules on top of a Store.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService returns a Service backed by store.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// NewID returns prefix followed by 12 random lowercase base32 characters.
func NewID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf) // crypto/rand.Read never fails on supported platforms
	return prefix + strings.ToLower(idEncoding.EncodeToString(buf))[:idChars]
}

// RegisterRepo validates and stores a repo.
func (s *Service) RegisterRepo(ctx context.Context, in RepoInput) (Repo, error) {
	in, err := normalizeRepo(in)
	if err != nil {
		return Repo{}, err
	}
	r := Repo{
		ID: NewID("r_"), Host: in.Host, URL: in.URL,
		ProtectedBranches: in.ProtectedBranches, MergeStrategy: in.MergeStrategy,
		PullRequests: in.PullRequests, Merger: in.Merger,
		CreatedAt: s.now().UTC(),
	}
	if err := s.store.CreateRepo(ctx, r); err != nil {
		if errors.Is(err, ErrConflict) {
			return Repo{}, fmt.Errorf("%w: repo %s is already registered", ErrConflict, in.URL)
		}
		return Repo{}, err
	}
	return r, nil
}

// GetRepo returns one repo.
func (s *Service) GetRepo(ctx context.Context, id string) (Repo, error) {
	r, err := s.store.GetRepo(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Repo{}, fmt.Errorf("%w: repo %s", ErrNotFound, id)
	}
	return r, err
}

// SetRepoMerging sets whether Plumbix watches a registered repository's
// pull requests and who merges them, under the registration rules; an
// empty merger takes the default for the new pull_requests value (WU-055).
func (s *Service) SetRepoMerging(ctx context.Context, id string, pullRequests bool, merger string) (Repo, error) {
	r, err := s.GetRepo(ctx, id)
	if err != nil {
		return Repo{}, err
	}
	var ps problems
	merger = normalizeMerging(r.Host, pullRequests, merger, &ps)
	if err := ps.err(); err != nil {
		return Repo{}, err
	}
	if err := s.store.UpdateRepoMerging(ctx, id, pullRequests, merger); err != nil {
		return Repo{}, err
	}
	r.PullRequests, r.Merger = pullRequests, merger
	return r, nil
}

// SetRepoMergeStrategy sets how Plumbix's own merges land on a registered
// repository (WU-064): squash, merge or rebase, the same rule as at
// registration. GitHub's merges of pull requests are not affected.
func (s *Service) SetRepoMergeStrategy(ctx context.Context, id, strategy string) (Repo, error) {
	r, err := s.GetRepo(ctx, id)
	if err != nil {
		return Repo{}, err
	}
	strategy = strings.TrimSpace(strategy)
	if err := CheckMergeStrategy(strategy); err != nil {
		return Repo{}, err
	}
	if err := s.store.UpdateRepoMergeStrategy(ctx, id, strategy); err != nil {
		return Repo{}, err
	}
	r.MergeStrategy = strategy
	return r, nil
}

// SetRepoPaused stops or resumes polling and pull-request intake for a
// repository (WU-065). Changes submitted through the API still run and
// merge: pausing is about what Plumbix watches, not what a person asks.
func (s *Service) SetRepoPaused(ctx context.Context, id string, paused bool) (Repo, error) {
	r, err := s.GetRepo(ctx, id)
	if err != nil {
		return Repo{}, err
	}
	if err := s.store.UpdateRepoPaused(ctx, id, paused); err != nil {
		return Repo{}, err
	}
	r.Paused = paused
	return r, nil
}

// RemoveRepo retires a repository at a person's request (WU-065): refused
// while a change of it is open or running (ErrRepoBusy), recorded as
// repo.removed naming who asked. The repository's changes stay readable.
func (s *Service) RemoveRepo(ctx context.Context, id, by string) error {
	by = strings.TrimSpace(by)
	switch {
	case by == "":
		return &InvalidError{Problems: []Problem{{Field: "by", Message: "is required: name the person removing the repository"}}}
	case utf8.RuneCountInString(by) > MaxSubmittedBy:
		return &InvalidError{Problems: []Problem{{Field: "by", Message: fmt.Sprintf("is longer than %d characters", MaxSubmittedBy)}}}
	}
	if _, err := s.GetRepo(ctx, id); err != nil {
		return err
	}
	return s.store.RemoveRepo(ctx, id, by, s.now().UTC())
}

// ListRepos returns every repo.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	return s.store.ListRepos(ctx)
}

// Submit validates and stores a new change in state open.
func (s *Service) Submit(ctx context.Context, in ChangeInput) (Change, error) {
	in, err := normalizeChange(in)
	if err != nil {
		return Change{}, err
	}
	if _, err := s.store.GetRepo(ctx, in.RepoID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Change{}, fmt.Errorf("%w: repo %s", ErrNotFound, in.RepoID)
		}
		return Change{}, err
	}
	criteria, ref := in.AcceptanceCriteria, in.CriteriaSetRef
	if in.CriteriaSet != "" {
		// The set supplies the criteria, frozen as of now (WU-066); the
		// submission's own list, if any, is not used.
		cs, err := s.store.GetCriteriaSet(ctx, in.CriteriaSet)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Change{}, fmt.Errorf("%w: criteria set %s", ErrNotFound, in.CriteriaSet)
			}
			return Change{}, err
		}
		criteria, ref = CopyCriteria(cs.Criteria), &CriteriaSetRef{ID: cs.ID, Owner: cs.Owner, Task: cs.Task, Version: cs.UpdatedAt}
	}
	now := s.now().UTC()
	c := Change{
		ID: NewID("c_"), RepoID: in.RepoID, SourceBranch: in.SourceBranch, TargetBranch: in.TargetBranch,
		Title: in.Title, Intent: in.Intent, AcceptanceCriteria: criteria,
		Constraints: in.Constraints, SubmittedBy: in.SubmittedBy, PlumbixBranch: in.PlumbixBranch, PullRequest: in.PullRequest,
		CriteriaSet: ref, State: Open, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateChange(ctx, c); err != nil {
		if errors.Is(err, ErrConflict) {
			return Change{}, fmt.Errorf("%w: another change is still open on branch %s", ErrConflict, in.SourceBranch)
		}
		return Change{}, err
	}
	return c, nil
}

// Get returns one change.
func (s *Service) Get(ctx context.Context, id string) (Change, error) {
	c, err := s.store.GetChange(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Change{}, fmt.Errorf("%w: change %s", ErrNotFound, id)
	}
	return c, err
}

// List returns changes matching f.
func (s *Service) List(ctx context.Context, f Filter) ([]Change, error) {
	if f.State != "" && !f.State.Valid() {
		var ps problems
		ps.add("state", "unknown state %q", f.State)
		return nil, ps.err()
	}
	return s.store.ListChanges(ctx, f)
}

// UpdatePullRequestText brings a change's title, intent and criteria up to
// date with the pull request it mirrors (WU-062), under the submission
// rules; a final change is not touched. No run starts: the next one reads
// the new text.
func (s *Service) UpdatePullRequestText(ctx context.Context, id, title, intent string, criteria []Criterion, set *CriteriaSetRef) (Change, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Change{}, err
	}
	if c.State.Final() {
		return Change{}, fmt.Errorf("%w: change %s is %s", ErrInvalidState, id, c.State)
	}
	in, err := normalizeChange(ChangeInput{RepoID: c.RepoID, SourceBranch: c.SourceBranch, TargetBranch: c.TargetBranch,
		Title: title, Intent: intent, AcceptanceCriteria: criteria, AllowNoCriteria: true, Constraints: c.Constraints, SubmittedBy: c.SubmittedBy})
	if err != nil {
		return Change{}, err
	}
	if err := s.store.UpdateChangeText(ctx, id, in.Title, in.Intent, in.AcceptanceCriteria, set, s.now().UTC()); err != nil {
		return Change{}, err
	}
	return s.Get(ctx, id)
}

// CopyCriteria is a set's criteria as a change carries them: the same
// text and checks, numbered afresh, independent of the set from then on.
// The pull-request intake uses it too.
func CopyCriteria(in []Criterion) []Criterion {
	out := make([]Criterion, len(in))
	for i, c := range in {
		out[i] = Criterion{ID: "AC-" + strconv.Itoa(i+1), Text: c.Text}
		if c.Check != nil {
			check := *c.Check
			check.Steps = append([]string(nil), c.Check.Steps...)
			out[i].Check = &check
		}
	}
	return out
}

// CreateCriteriaSet stores acceptance criteria written with a
// specification (WU-066, A43) and returns the set with its id.
func (s *Service) CreateCriteriaSet(ctx context.Context, in CriteriaSetInput) (CriteriaSet, error) {
	in, err := normalizeCriteriaSet(in)
	if err != nil {
		return CriteriaSet{}, err
	}
	now := s.now().UTC()
	cs := CriteriaSet{ID: NewID("cs_"), Task: in.Task, Title: in.Title, Owner: in.Owner, Source: in.Source, Criteria: in.Criteria, UsedBy: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateCriteriaSet(ctx, cs); err != nil {
		return CriteriaSet{}, err
	}
	return cs, nil
}

// UpdateCriteriaSet replaces a set's title, owner, source and criteria;
// a set a change has used is frozen (ErrCriteriaSetUsed).
func (s *Service) UpdateCriteriaSet(ctx context.Context, id string, in CriteriaSetInput) (CriteriaSet, error) {
	cur, err := s.GetCriteriaSet(ctx, id)
	if err != nil {
		return CriteriaSet{}, err
	}
	if cur.Frozen || len(cur.UsedBy) > 0 {
		used := strings.Join(cur.UsedBy, ", ")
		if used == "" {
			used = "a change since closed or edited away"
		}
		return CriteriaSet{}, fmt.Errorf("%w: set %s was used by %s; a new set is a new id", ErrCriteriaSetUsed, id, used)
	}
	in, err = normalizeCriteriaSet(in)
	if err != nil {
		return CriteriaSet{}, err
	}
	cs := CriteriaSet{ID: id, Task: in.Task, Title: in.Title, Owner: in.Owner, Source: in.Source, Criteria: in.Criteria, UsedBy: []string{}, CreatedAt: cur.CreatedAt, UpdatedAt: s.now().UTC()}
	if err := s.store.UpdateCriteriaSet(ctx, cs); err != nil {
		return CriteriaSet{}, err
	}
	return s.GetCriteriaSet(ctx, id)
}

// GetCriteriaSet returns one set, with the changes that used it.
func (s *Service) GetCriteriaSet(ctx context.Context, id string) (CriteriaSet, error) {
	cs, err := s.store.GetCriteriaSet(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return CriteriaSet{}, fmt.Errorf("%w: criteria set %s", ErrNotFound, id)
		}
		return CriteriaSet{}, err
	}
	return cs, nil
}

// ListCriteriaSets lists sets, newest first, those of one task when given.
func (s *Service) ListCriteriaSets(ctx context.Context, task string) ([]CriteriaSet, error) {
	return s.store.ListCriteriaSets(ctx, strings.TrimSpace(task))
}

// Close moves a change to closed unless it is already merged or closed.
func (s *Service) Close(ctx context.Context, id string) (Change, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Change{}, err
	}
	if c.State.Final() {
		return Change{}, fmt.Errorf("%w: change %s is already %s", ErrInvalidState, id, c.State)
	}
	if err := s.store.SetChangeState(ctx, id, c.State, Closed, s.now().UTC()); err != nil {
		if errors.Is(err, ErrConflict) {
			return Change{}, fmt.Errorf("%w: change %s changed state while closing; retry", ErrConflict, id)
		}
		return Change{}, err
	}
	return s.Get(ctx, id)
}
