package change

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
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
	now := s.now().UTC()
	c := Change{
		ID: NewID("c_"), RepoID: in.RepoID, SourceBranch: in.SourceBranch, TargetBranch: in.TargetBranch,
		Title: in.Title, Intent: in.Intent, AcceptanceCriteria: in.AcceptanceCriteria,
		Constraints: in.Constraints, SubmittedBy: in.SubmittedBy, PlumbixBranch: in.PlumbixBranch, PullRequest: in.PullRequest,
		State: Open, CreatedAt: now, UpdatedAt: now,
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
