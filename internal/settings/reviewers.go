package settings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/store"
)

// Reviewer kinds (WU-028, A28).
const (
	KindAnthropic = "anthropic"
	KindOpenAI    = "openai" // any OpenAI-compatible endpoint, by base URL
)

// Setting keys for profiles.
const (
	keyDefaultReviewer   = "reviewers.default"
	keyReviewersMigrated = "reviewers.migrated"
)

var (
	reviewerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	secretNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Reviewer is a named model configuration a validator step can be bound
// to with reviewer: <name>. The key itself is the secret named by
// KeySecret, never part of the profile.
type Reviewer struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	BaseURL   string    `json:"base_url"`   // openai only
	Model     string    `json:"model"`      // empty means the kind's default
	Tools     string    `json:"tools"`      // read-only or full, where a reviewer has tools
	KeySecret string    `json:"key_secret"` // the secret holding the API key
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultKeySecret is the secret name a profile's key goes under unless the
// operator names another: PLUMBIX_REVIEWER_<NAME>_KEY.
func DefaultKeySecret(name string) string {
	return "PLUMBIX_REVIEWER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_KEY"
}

// Reviewers lists every profile, by name.
func (s *Service) Reviewers(ctx context.Context) ([]Reviewer, error) {
	rows, err := s.db.ListReviewerProfiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Reviewer, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

// Reviewer returns the profile called name, or the default profile when
// name is empty. ok is false when there is no such profile, which is not
// an error: the caller decides what no profile means.
func (s *Service) Reviewer(ctx context.Context, name string) (Reviewer, bool, error) {
	if name == "" {
		var err error
		if name, err = s.DefaultReviewer(ctx); err != nil || name == "" {
			return Reviewer{}, false, err
		}
	}
	row, err := s.db.GetReviewerProfile(ctx, name)
	if errors.Is(err, change.ErrNotFound) {
		return Reviewer{}, false, nil
	}
	if err != nil {
		return Reviewer{}, false, err
	}
	return fromRow(row), true, nil
}

// PutReviewer validates and stores a profile, creating or replacing it, and
// returns it as stored (defaults filled in).
func (s *Service) PutReviewer(ctx context.Context, r Reviewer) (Reviewer, error) {
	r.Name = strings.TrimSpace(r.Name)
	r.Kind = strings.TrimSpace(r.Kind)
	r.BaseURL = strings.TrimSpace(r.BaseURL)
	r.Model = strings.TrimSpace(r.Model)
	r.Tools = strings.TrimSpace(r.Tools)
	r.KeySecret = strings.TrimSpace(r.KeySecret)
	var problems []change.Problem
	add := func(field, msg string) { problems = append(problems, change.Problem{Field: field, Message: msg}) }
	if !reviewerNameRe.MatchString(r.Name) {
		add("name", "must be lowercase letters, digits and dashes, start with a letter or digit, at most 32 characters")
	}
	switch r.Kind {
	case KindAnthropic:
		r.BaseURL = ""
	case KindOpenAI:
		if r.BaseURL == "" {
			add("base_url", "is required for an OpenAI-compatible reviewer: the chat-completions base such as https://api.openai.com/v1")
		} else if err := checkBaseURL(r.BaseURL); err != nil {
			add("base_url", err.Error())
		}
	default:
		add("kind", fmt.Sprintf("must be %s or %s", KindAnthropic, KindOpenAI))
	}
	if r.Model != "" && !modelRe.MatchString(r.Model) {
		add("model", "lowercase letters, digits, dots and dashes only")
	}
	switch r.Tools {
	case "":
		r.Tools = review.ToolsReadOnly
	case review.ToolsReadOnly, review.ToolsFull:
	default:
		add("tools", fmt.Sprintf("must be %s or %s", review.ToolsReadOnly, review.ToolsFull))
	}
	if r.KeySecret == "" {
		if reviewerNameRe.MatchString(r.Name) {
			r.KeySecret = DefaultKeySecret(r.Name)
		}
	} else if !secretNameRe.MatchString(r.KeySecret) {
		add("key_secret", "must be a secret name: letters, digits and underscores, not starting with a digit")
	}
	if len(problems) > 0 {
		return Reviewer{}, &change.InvalidError{Problems: problems}
	}
	r.UpdatedAt = s.now().UTC()
	if err := s.db.PutReviewerProfile(ctx, toRow(r)); err != nil {
		return Reviewer{}, err
	}
	return r, nil
}

// DeleteReviewer removes a profile; the default is cleared if it was it.
// change.ErrNotFound when there is no such profile.
func (s *Service) DeleteReviewer(ctx context.Context, name string) error {
	if err := s.db.DeleteReviewerProfile(ctx, name); err != nil {
		return err
	}
	if cur, err := s.DefaultReviewer(ctx); err != nil {
		return err
	} else if cur == name {
		return s.db.DeleteSetting(ctx, keyDefaultReviewer)
	}
	return nil
}

// DefaultReviewer returns the default profile's name, or "".
func (s *Service) DefaultReviewer(ctx context.Context) (string, error) {
	v, _, _, err := s.db.GetSetting(ctx, keyDefaultReviewer)
	return v, err
}

// SetDefaultReviewer makes name the default; it must exist. Empty clears.
func (s *Service) SetDefaultReviewer(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return s.db.DeleteSetting(ctx, keyDefaultReviewer)
	}
	if _, err := s.db.GetReviewerProfile(ctx, name); err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return &change.InvalidError{Problems: []change.Problem{{Field: "default_reviewer", Message: fmt.Sprintf("there is no reviewer profile called %q", name)}}}
		}
		return err
	}
	return s.db.SetSetting(ctx, keyDefaultReviewer, name, s.now().UTC())
}

// EnsureReviewers runs once per installation, at server start: the global
// AI settings of earlier versions become a profile called "default" (and
// the default), or a fresh install gets the built-in one. Afterwards the
// operator owns the list; an empty list stays empty.
func (s *Service) EnsureReviewers(ctx context.Context) error {
	if _, _, done, err := s.db.GetSetting(ctx, keyReviewersMigrated); err != nil || done {
		return err
	}
	r := Reviewer{Name: "default", Kind: KindAnthropic, Tools: review.ToolsReadOnly, KeySecret: "ANTHROPIC_API_KEY"}
	get := func(key string) (string, error) {
		v, _, _, err := s.db.GetSetting(ctx, key)
		return v, err
	}
	provider, err := get(keyProvider)
	if err != nil {
		return err
	}
	if provider == KindOpenAI {
		r.Kind, r.KeySecret = KindOpenAI, "OPENAI_API_KEY"
		if r.BaseURL, err = get(keyBaseURL); err != nil {
			return err
		}
	}
	if r.Model, err = get(keyModel); err != nil {
		return err
	}
	if tools, err := get(keyTools); err != nil {
		return err
	} else if tools != "" {
		r.Tools = tools
	}
	if r.Kind == KindOpenAI && r.BaseURL == "" {
		r.BaseURL = "https://api.openai.com/v1"
	}
	if _, err := s.PutReviewer(ctx, r); err != nil {
		return fmt.Errorf("migrate AI settings into a reviewer profile: %w", err)
	}
	if err := s.SetDefaultReviewer(ctx, r.Name); err != nil {
		return err
	}
	for _, key := range []string{keyProvider, keyBaseURL, keyModel, keyTools} {
		if err := s.db.DeleteSetting(ctx, key); err != nil {
			return err
		}
	}
	return s.db.SetSetting(ctx, keyReviewersMigrated, "1", s.now().UTC())
}

// checkBaseURL accepts an http(s) URL with a host and no credentials.
func checkBaseURL(raw string) error {
	if len(raw) > 200 {
		return fmt.Errorf("is %d characters; the limit is 200", len(raw))
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(raw, " \t\"'") {
		return fmt.Errorf("%q must be an http(s) URL such as https://api.openai.com/v1", raw)
	}
	return nil
}

func toRow(r Reviewer) store.ReviewerProfile {
	return store.ReviewerProfile{Name: r.Name, Kind: r.Kind, BaseURL: r.BaseURL, Model: r.Model, Tools: r.Tools, KeySecret: r.KeySecret, UpdatedAt: r.UpdatedAt}
}

func fromRow(p store.ReviewerProfile) Reviewer {
	return Reviewer{Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Model: p.Model, Tools: p.Tools, KeySecret: p.KeySecret, UpdatedAt: p.UpdatedAt}
}
