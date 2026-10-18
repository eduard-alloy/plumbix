// Package settings holds what an operator configures at runtime: review
// guidance for validators, the AI model, and when each secret was last set.
// Secret values themselves live in the secrets file (package secrets).
package settings

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/store"
)

// MaxGuidanceChars bounds a guidance text: it is prompt material.
const MaxGuidanceChars = 4000

// Setting keys.
const (
	keyModel      = "ai.model"
	keyTools      = "ai.tools"
	keyProvider   = "ai.provider"
	keyBaseURL    = "ai.base_url"
	keyGuidance   = "guidance.global"
	prefixRepo    = "guidance.repo."
	prefixSecret  = "secret."
	modelKeyShape = `^[a-z0-9][a-z0-9.-]{0,80}$`
)

var modelRe = regexp.MustCompile(modelKeyShape)

// Guidance is what validators receive as operator instructions.
type Guidance struct {
	Global     string `json:"global,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// Secret describes a well-known secret key.
type Secret struct {
	Key     string
	Purpose string
	Host    string // host kind whose token this is, if any
}

// KnownSecrets lists the keys Plumbix itself reads, in display order.
func KnownSecrets() []Secret {
	return []Secret{
		{Key: "PLUMBIX_GITHUB_TOKEN", Purpose: "Default token for every GitHub repository: contents read and write; fetches, pushes merges, checks expiry. A fine-grained token covers only the repositories chosen for it; a repository can carry its own token on the Repositories page", Host: "github"},
		{Key: "PLUMBIX_AZURE_DEVOPS_TOKEN", Purpose: "Default personal access token for every Azure DevOps repository, code read and write; a repository can carry its own token on the Repositories page", Host: "azuredevops"},
		{Key: "PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES", Purpose: "Expiry date of the Azure DevOps token, YYYY-MM-DD, for the warning a week before"},
		{Key: "ANTHROPIC_API_KEY", Purpose: "Used by the AI validator step through the pipeline's secrets list, with the anthropic provider"},
		{Key: "OPENAI_API_KEY", Purpose: "Used by the AI validator step with the openai provider: the key of whichever OpenAI-compatible vendor the base URL points at (OpenAI, Gemini, xAI, Moonshot, DeepSeek, Groq, a local server)"},
	}
}

// Service reads and writes settings in the store.
type Service struct {
	db  *store.DB
	now func() time.Time
}

// New returns a Service over db.
func New(db *store.DB) *Service {
	return &Service{db: db, now: time.Now}
}

// Guidance returns the global guidance and the repository's own.
func (s *Service) Guidance(ctx context.Context, repoID string) (Guidance, error) {
	global, _, _, err := s.db.GetSetting(ctx, keyGuidance)
	if err != nil {
		return Guidance{}, err
	}
	repo := ""
	if repoID != "" {
		if repo, _, _, err = s.db.GetSetting(ctx, prefixRepo+repoID); err != nil {
			return Guidance{}, err
		}
	}
	return Guidance{Global: global, Repository: repo}, nil
}

// SetGlobalGuidance stores the global guidance; empty clears it.
func (s *Service) SetGlobalGuidance(ctx context.Context, text string) error {
	return s.setText(ctx, keyGuidance, text)
}

// SetRepoGuidance stores one repository's guidance; empty clears it.
func (s *Service) SetRepoGuidance(ctx context.Context, repoID, text string) error {
	return s.setText(ctx, prefixRepo+repoID, text)
}

func (s *Service) setText(ctx context.Context, key, text string) error {
	text = strings.TrimSpace(text)
	if len(text) > MaxGuidanceChars {
		return fmt.Errorf("guidance is %d characters; the limit is %d", len(text), MaxGuidanceChars)
	}
	if text == "" {
		return s.db.DeleteSetting(ctx, key)
	}
	return s.db.SetSetting(ctx, key, text, s.now().UTC())
}

// Providers the AI validator can ask (WU-025).
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// AIProvider returns the provider the AI validator asks: anthropic unless
// the operator chose openai.
func (s *Service) AIProvider(ctx context.Context) (string, error) {
	v, _, _, err := s.db.GetSetting(ctx, keyProvider)
	if err != nil || v == "" {
		return ProviderAnthropic, err
	}
	return v, nil
}

// SetAIProvider stores the provider: anthropic or openai.
func (s *Service) SetAIProvider(ctx context.Context, provider string) error {
	switch strings.TrimSpace(provider) {
	case ProviderAnthropic, "":
		return s.db.DeleteSetting(ctx, keyProvider)
	case ProviderOpenAI:
		return s.db.SetSetting(ctx, keyProvider, ProviderOpenAI, s.now().UTC())
	}
	return fmt.Errorf("provider %q: must be %s or %s", provider, ProviderAnthropic, ProviderOpenAI)
}

// AIBaseURL returns the openai provider's base URL, or "".
func (s *Service) AIBaseURL(ctx context.Context) (string, error) {
	v, _, _, err := s.db.GetSetting(ctx, keyBaseURL)
	return v, err
}

// SetAIBaseURL stores the base URL of an OpenAI-compatible endpoint; empty
// clears it. http is allowed, for local servers; the URL must have a host.
func (s *Service) SetAIBaseURL(ctx context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return s.db.DeleteSetting(ctx, keyBaseURL)
	}
	if len(raw) > 200 {
		return fmt.Errorf("base URL is %d characters; the limit is 200", len(raw))
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(raw, " \t\"'") {
		return fmt.Errorf("base URL %q: must be an http(s) URL such as https://api.openai.com/v1", raw)
	}
	return s.db.SetSetting(ctx, keyBaseURL, raw, s.now().UTC())
}

// AITools returns the tool policy for external reviewers and the Claude Code
// validator: review.ToolsReadOnly unless the operator chose review.ToolsFull.
func (s *Service) AITools(ctx context.Context) (string, error) {
	v, _, _, err := s.db.GetSetting(ctx, keyTools)
	if err != nil || v == "" {
		return review.ToolsReadOnly, err
	}
	return v, nil
}

// SetAITools stores the tool policy: read-only or full.
func (s *Service) SetAITools(ctx context.Context, tools string) error {
	switch strings.TrimSpace(tools) {
	case review.ToolsReadOnly:
		return s.db.DeleteSetting(ctx, keyTools)
	case review.ToolsFull:
		return s.db.SetSetting(ctx, keyTools, review.ToolsFull, s.now().UTC())
	}
	return fmt.Errorf("tools %q: must be %s or %s", tools, review.ToolsReadOnly, review.ToolsFull)
}

// AIModel returns the model validator steps are told to use, or "".
func (s *Service) AIModel(ctx context.Context) (string, error) {
	v, _, _, err := s.db.GetSetting(ctx, keyModel)
	return v, err
}

// SetAIModel stores the model name; empty clears it.
func (s *Service) SetAIModel(ctx context.Context, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return s.db.DeleteSetting(ctx, keyModel)
	}
	if !modelRe.MatchString(model) {
		return fmt.Errorf("model name %q: lowercase letters, digits, dots and dashes only", model)
	}
	return s.db.SetSetting(ctx, keyModel, model, s.now().UTC())
}

// MarkSecret records that a secret was set now.
func (s *Service) MarkSecret(ctx context.Context, key string) error {
	return s.db.SetSetting(ctx, prefixSecret+key, "set", s.now().UTC())
}

// ForgetSecret removes a secret's record.
func (s *Service) ForgetSecret(ctx context.Context, key string) error {
	return s.db.DeleteSetting(ctx, prefixSecret+key)
}

// SecretUpdated returns when a secret was last set through Plumbix.
func (s *Service) SecretUpdated(ctx context.Context, key string) (time.Time, bool, error) {
	_, when, ok, err := s.db.GetSetting(ctx, prefixSecret+key)
	return when, ok, err
}
