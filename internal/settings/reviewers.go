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

// Reviewer kinds (WU-028, A28; A29 adds claude-code).
const (
	KindAnthropic  = "anthropic"   // the Anthropic API, from the container validator; billed per token
	KindOpenAI     = "openai"      // any OpenAI-compatible endpoint, by base URL; billed by its vendor
	KindClaudeCode = "claude-code" // plumbix review --watch on a person's machine, on its Claude subscription
)

// Family is where a reviewer runs: an API kind judges from the container
// validator, claude-code answers external steps. A step binds within its
// family only, and each family has its own default (A29).
type Family string

const (
	FamilyAPI      Family = "api"
	FamilyExternal Family = "external"
)

// Defaults names the default profile of each family; empty for none.
type Defaults struct {
	API      string `json:"api"`
	External string `json:"external"`
}

// Setting keys for profiles.
const (
	keyDefaultReviewer   = "reviewers.default"          // the api family's default
	keyDefaultExternal   = "reviewers.default_external" // the external family's default
	keyReviewersMigrated = "reviewers.migrated"
	keyReviewersSplit    = "reviewers.split" // A29: claude-code separated from anthropic
)

var (
	reviewerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	secretNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Reviewer is a named model configuration a validator step can be bound
// to with reviewer: <name>. The key itself is the secret named by
// KeySecret, never part of the profile; a claude-code profile has none.
type Reviewer struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	BaseURL   string    `json:"base_url"`   // openai only
	Model     string    `json:"model"`      // empty means the kind's default
	Tools     string    `json:"tools"`      // read-only or full, where a reviewer has tools
	KeySecret string    `json:"key_secret"` // the secret holding the API key
	UpdatedAt time.Time `json:"updated_at"`
}

// Family says where the profile runs.
func (r Reviewer) Family() Family {
	if r.Kind == KindClaudeCode {
		return FamilyExternal
	}
	return FamilyAPI
}

// KindLabel is the kind as the page and logs name it.
func KindLabel(kind string) string {
	switch kind {
	case KindAnthropic:
		return "Anthropic API"
	case KindOpenAI:
		return "OpenAI-compatible API"
	case KindClaudeCode:
		return "Claude Code"
	}
	return kind
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

// Reviewer returns the profile called name, or family's default profile
// when name is empty. ok is false when there is no such profile, which is
// not an error: the caller decides what no profile means, and checks that
// a named profile's family fits.
func (s *Service) Reviewer(ctx context.Context, name string, family Family) (Reviewer, bool, error) {
	if name == "" {
		d, err := s.Defaults(ctx)
		if err != nil {
			return Reviewer{}, false, err
		}
		if name = d.API; family == FamilyExternal {
			name = d.External
		}
		if name == "" {
			return Reviewer{}, false, nil
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
	case KindClaudeCode:
		if r.BaseURL != "" {
			add("base_url", "is not for Claude Code: it reaches Anthropic through the machine's own Claude login")
		}
		if r.KeySecret != "" {
			add("key_secret", "is not for Claude Code: it uses the subscription of the machine running plumbix review, no key")
		}
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
		if reviewerNameRe.MatchString(r.Name) && r.Kind != KindClaudeCode {
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

// DeleteReviewer removes a profile; a default it held is cleared.
// change.ErrNotFound when there is no such profile.
func (s *Service) DeleteReviewer(ctx context.Context, name string) error {
	if err := s.db.DeleteReviewerProfile(ctx, name); err != nil {
		return err
	}
	d, err := s.Defaults(ctx)
	if err != nil {
		return err
	}
	if d.API == name {
		if err := s.db.DeleteSetting(ctx, keyDefaultReviewer); err != nil {
			return err
		}
	}
	if d.External == name {
		return s.db.DeleteSetting(ctx, keyDefaultExternal)
	}
	return nil
}

// Defaults returns each family's default profile name.
func (s *Service) Defaults(ctx context.Context) (Defaults, error) {
	api, _, _, err := s.db.GetSetting(ctx, keyDefaultReviewer)
	if err != nil {
		return Defaults{}, err
	}
	ext, _, _, err := s.db.GetSetting(ctx, keyDefaultExternal)
	return Defaults{API: api, External: ext}, err
}

// SetDefaultReviewer makes name the default of its own family; it must exist.
func (s *Service) SetDefaultReviewer(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	row, err := s.db.GetReviewerProfile(ctx, name)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return &change.InvalidError{Problems: []change.Problem{{Field: "default_reviewer", Message: fmt.Sprintf("there is no reviewer profile called %q", name)}}}
		}
		return err
	}
	return s.db.SetSetting(ctx, defaultKey(fromRow(row).Family()), name, s.now().UTC())
}

// ClearDefaultReviewer leaves family with no default.
func (s *Service) ClearDefaultReviewer(ctx context.Context, family Family) error {
	return s.db.DeleteSetting(ctx, defaultKey(family))
}

func defaultKey(family Family) string {
	if family == FamilyExternal {
		return keyDefaultExternal
	}
	return keyDefaultReviewer
}

// EnsureReviewers runs at server start and acts once per installation and
// step. First (marker reviewers.migrated) the global AI settings of earlier
// versions become an api profile and the default of that family, or a
// fresh install gets the built-in anthropic one. Then (marker
// reviewers.split, A29) the external family gets a claude-code profile:
// from the api default's model and tools when that was anthropic, since
// the loop used those too, else with the built-in values; an api profile
// still called "default" is renamed, since there are two defaults now.
// Afterwards the operator owns the list; an emptied family stays empty.
func (s *Service) EnsureReviewers(ctx context.Context) error {
	if err := s.migrateLegacyAISettings(ctx); err != nil {
		return err
	}
	return s.splitClaudeCode(ctx)
}

func (s *Service) migrateLegacyAISettings(ctx context.Context) error {
	if _, _, done, err := s.db.GetSetting(ctx, keyReviewersMigrated); err != nil || done {
		return err
	}
	r := Reviewer{Name: "claude-api", Kind: KindAnthropic, Tools: review.ToolsReadOnly, KeySecret: "ANTHROPIC_API_KEY"}
	get := func(key string) (string, error) {
		v, _, _, err := s.db.GetSetting(ctx, key)
		return v, err
	}
	provider, err := get(keyProvider)
	if err != nil {
		return err
	}
	if provider == KindOpenAI {
		r.Name, r.Kind, r.KeySecret = "openai-api", KindOpenAI, "OPENAI_API_KEY"
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
	// The loop used the same tools (and the model, when it was Claude's).
	code := Reviewer{Name: "claude-code", Kind: KindClaudeCode, Tools: r.Tools}
	if r.Kind == KindAnthropic {
		code.Model = r.Model
	}
	if _, err := s.PutReviewer(ctx, code); err != nil {
		return err
	}
	if err := s.SetDefaultReviewer(ctx, code.Name); err != nil {
		return err
	}
	for _, key := range []string{keyProvider, keyBaseURL, keyModel, keyTools} {
		if err := s.db.DeleteSetting(ctx, key); err != nil {
			return err
		}
	}
	if err := s.db.SetSetting(ctx, keyReviewersMigrated, "1", s.now().UTC()); err != nil {
		return err
	}
	return s.db.SetSetting(ctx, keyReviewersSplit, "1", s.now().UTC())
}

// splitClaudeCode upgrades an installation migrated under A28, where one
// anthropic profile called "default" served the loop too.
func (s *Service) splitClaudeCode(ctx context.Context) error {
	if _, _, done, err := s.db.GetSetting(ctx, keyReviewersSplit); err != nil || done {
		return err
	}
	d, err := s.Defaults(ctx)
	if err != nil {
		return err
	}
	code := Reviewer{Name: "claude-code", Kind: KindClaudeCode, Tools: review.ToolsReadOnly}
	if api, ok, err := s.Reviewer(ctx, d.API, FamilyAPI); err != nil {
		return err
	} else if ok {
		code.Tools = api.Tools
		if api.Kind == KindAnthropic {
			code.Model = api.Model
		}
		if api.Name == "default" {
			renamed := api
			renamed.Name = "claude-api"
			if api.Kind == KindOpenAI {
				renamed.Name = "openai-api"
			}
			if _, err := s.PutReviewer(ctx, renamed); err != nil {
				return err
			}
			if err := s.SetDefaultReviewer(ctx, renamed.Name); err != nil {
				return err
			}
			if err := s.db.DeleteReviewerProfile(ctx, "default"); err != nil {
				return err
			}
		}
	}
	if _, ok, err := s.Reviewer(ctx, code.Name, FamilyExternal); err != nil {
		return err
	} else if !ok {
		if _, err := s.PutReviewer(ctx, code); err != nil {
			return err
		}
	}
	if err := s.SetDefaultReviewer(ctx, code.Name); err != nil {
		return err
	}
	return s.db.SetSetting(ctx, keyReviewersSplit, "1", s.now().UTC())
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
