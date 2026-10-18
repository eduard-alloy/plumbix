package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/review"
)

func TestReviewerProfilesAreStoredValidatedAndDefaulted(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	// Nothing yet: no profiles, no default.
	list, err := s.Reviewers(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("reviewers = %v %v, want none", list, err)
	}
	if _, ok, err := s.Reviewer(ctx, ""); err != nil || ok {
		t.Fatalf("default reviewer = ok %v %v, want none", ok, err)
	}

	// An openai profile with a base URL; the key secret defaults from the name.
	grok, err := s.PutReviewer(ctx, Reviewer{Name: "grok-4", Kind: KindOpenAI, BaseURL: "https://api.x.ai/v1", Model: "grok-4"})
	if err != nil {
		t.Fatal(err)
	}
	if grok.KeySecret != "PLUMBIX_REVIEWER_GROK_4_KEY" || grok.Tools != review.ToolsReadOnly || grok.UpdatedAt.IsZero() {
		t.Errorf("stored profile = %+v", grok)
	}
	// An anthropic profile sharing the known key; a put replaces.
	if _, err := s.PutReviewer(ctx, Reviewer{Name: "claude", Kind: KindAnthropic, Model: "claude-sonnet-5-5", Tools: review.ToolsFull, KeySecret: "ANTHROPIC_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	claude, err := s.PutReviewer(ctx, Reviewer{Name: "claude", Kind: KindAnthropic, Model: "claude-haiku-5-5", Tools: review.ToolsFull, KeySecret: "ANTHROPIC_API_KEY"})
	if err != nil || claude.Model != "claude-haiku-5-5" {
		t.Fatalf("replaced profile = %+v %v", claude, err)
	}
	list, _ = s.Reviewers(ctx)
	if len(list) != 2 || list[0].Name != "claude" || list[1].Name != "grok-4" {
		t.Errorf("reviewers = %+v, want claude then grok-4 by name", list)
	}

	// The default: set, read by empty name, cleared when its profile goes.
	if err := s.SetDefaultReviewer(ctx, "grok-4"); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.DefaultReviewer(ctx); name != "grok-4" {
		t.Errorf("default = %q", name)
	}
	if r, ok, err := s.Reviewer(ctx, ""); err != nil || !ok || r.Name != "grok-4" {
		t.Errorf("Reviewer(\"\") = %+v %v %v", r, ok, err)
	}
	if r, ok, err := s.Reviewer(ctx, "claude"); err != nil || !ok || r.Model != "claude-haiku-5-5" {
		t.Errorf("Reviewer(claude) = %+v %v %v", r, ok, err)
	}
	if _, ok, err := s.Reviewer(ctx, "nobody"); err != nil || ok {
		t.Errorf("Reviewer(nobody) = ok %v %v, want not found without error", ok, err)
	}
	if err := s.SetDefaultReviewer(ctx, "nobody"); err == nil {
		t.Error("a default that does not exist was accepted")
	}
	if err := s.DeleteReviewer(ctx, "grok-4"); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.DefaultReviewer(ctx); name != "" {
		t.Errorf("default after deleting it = %q, want cleared", name)
	}
	if err := s.DeleteReviewer(ctx, "grok-4"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if err := s.SetDefaultReviewer(ctx, ""); err != nil {
		t.Errorf("clearing the default: %v", err)
	}

	// Validation: each refusal names the field.
	for _, bad := range []struct {
		r    Reviewer
		want string
	}{
		{Reviewer{Name: "Grok", Kind: KindOpenAI, BaseURL: "https://api.x.ai/v1"}, "name"},
		{Reviewer{Name: "", Kind: KindOpenAI}, "name"},
		{Reviewer{Name: "x", Kind: "bedrock"}, "kind"},
		{Reviewer{Name: "x", Kind: KindOpenAI}, "base_url"},
		{Reviewer{Name: "x", Kind: KindOpenAI, BaseURL: "ftp://x"}, "base_url"},
		{Reviewer{Name: "x", Kind: KindAnthropic, Model: "Not A Model"}, "model"},
		{Reviewer{Name: "x", Kind: KindAnthropic, Tools: "some"}, "tools"},
		{Reviewer{Name: "x", Kind: KindAnthropic, KeySecret: "lower case"}, "key_secret"},
	} {
		_, err := s.PutReviewer(ctx, bad.r)
		var inv *change.InvalidError
		if !errors.As(err, &inv) || len(inv.Problems) == 0 || inv.Problems[0].Field != bad.want {
			t.Errorf("PutReviewer(%+v) = %v, want a problem on %s", bad.r, err, bad.want)
		}
	}
}

func TestLegacyAISettingsBecomeTheDefaultProfileOnce(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	// The WU-025 settings as an upgraded server finds them.
	for k, v := range map[string]string{keyProvider: "openai", keyBaseURL: "https://api.groq.com/openai/v1", keyModel: "llama-4", keyTools: review.ToolsFull} {
		if err := s.db.SetSetting(ctx, k, v, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.Reviewer(ctx, "")
	if err != nil || !ok {
		t.Fatalf("default after migration: %+v %v %v", r, ok, err)
	}
	want := Reviewer{Name: "default", Kind: KindOpenAI, BaseURL: "https://api.groq.com/openai/v1", Model: "llama-4", Tools: review.ToolsFull, KeySecret: "OPENAI_API_KEY"}
	r.UpdatedAt = time.Time{}
	if r != want {
		t.Errorf("migrated profile = %+v\nwant %+v", r, want)
	}
	for _, k := range []string{keyProvider, keyBaseURL, keyModel, keyTools} {
		if _, _, set, _ := s.db.GetSetting(ctx, k); set {
			t.Errorf("legacy setting %s still present", k)
		}
	}
	// Running again, after the operator removed every profile, creates nothing.
	if err := s.DeleteReviewer(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Reviewers(ctx); len(list) != 0 {
		t.Errorf("profiles recreated on the second start: %+v", list)
	}

	// A fresh install gets the built-in default: anthropic, read-only, the known key.
	fresh := newService(t)
	if err := fresh.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	r, ok, _ = fresh.Reviewer(ctx, "")
	if !ok || r.Name != "default" || r.Kind != KindAnthropic || r.Tools != review.ToolsReadOnly || r.KeySecret != "ANTHROPIC_API_KEY" || r.Model != "" {
		t.Errorf("fresh default = %+v %v", r, ok)
	}
}

func TestDefaultKeySecretName(t *testing.T) {
	for in, want := range map[string]string{"grok-4": "PLUMBIX_REVIEWER_GROK_4_KEY", "claude": "PLUMBIX_REVIEWER_CLAUDE_KEY", "a-b-c": "PLUMBIX_REVIEWER_A_B_C_KEY"} {
		if got := DefaultKeySecret(in); got != want {
			t.Errorf("DefaultKeySecret(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.HasPrefix(DefaultKeySecret("x"), "PLUMBIX_REVIEWER_") {
		t.Error("prefix")
	}
}
