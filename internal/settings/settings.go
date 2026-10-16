// Package settings holds what an operator configures at runtime: review
// guidance for validators, the AI model, and when each secret was last set.
// Secret values themselves live in the secrets file (package secrets).
package settings

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/store"
)

// MaxGuidanceChars bounds a guidance text: it is prompt material.
const MaxGuidanceChars = 4000

// Setting keys.
const (
	keyModel      = "ai.model"
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
		{Key: "PLUMBIX_GITHUB_TOKEN", Purpose: "GitHub fine-grained token with contents read and write; fetches, pushes merges, checks expiry", Host: "github"},
		{Key: "PLUMBIX_AZURE_DEVOPS_TOKEN", Purpose: "Azure DevOps personal access token with code read and write", Host: "azuredevops"},
		{Key: "PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES", Purpose: "Expiry date of the Azure DevOps token, YYYY-MM-DD, for the warning a week before"},
		{Key: "ANTHROPIC_API_KEY", Purpose: "Used by the AI validator step through the pipeline's secrets list"},
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
