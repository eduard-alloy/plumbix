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

	// Nothing yet: no profiles, no defaults.
	list, err := s.Reviewers(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("reviewers = %v %v, want none", list, err)
	}
	for _, family := range []Family{FamilyAPI, FamilyExternal} {
		if _, ok, err := s.Reviewer(ctx, "", family); err != nil || ok {
			t.Fatalf("default %s reviewer = ok %v %v, want none", family, ok, err)
		}
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

	// A claude-code profile: the external family, no key.
	code, err := s.PutReviewer(ctx, Reviewer{Name: "mac", Kind: KindClaudeCode, Model: "claude-sonnet-5-5", Tools: review.ToolsFull})
	if err != nil || code.KeySecret != "" || code.Family() != FamilyExternal {
		t.Fatalf("claude-code profile = %+v %v, want no key secret and the external family", code, err)
	}
	if grok.Family() != FamilyAPI || claude.Family() != FamilyAPI {
		t.Errorf("api profiles report family %s %s", grok.Family(), claude.Family())
	}

	// Defaults: one per family, set, read by empty name, cleared when the
	// profile goes; a profile of the other family is refused.
	if err := s.SetDefaultReviewer(ctx, "grok-4"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultReviewer(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Defaults(ctx); d.API != "grok-4" || d.External != "mac" {
		t.Errorf("defaults = %+v", d)
	}
	if r, ok, err := s.Reviewer(ctx, "", FamilyAPI); err != nil || !ok || r.Name != "grok-4" {
		t.Errorf("Reviewer(\"\", api) = %+v %v %v", r, ok, err)
	}
	if r, ok, err := s.Reviewer(ctx, "", FamilyExternal); err != nil || !ok || r.Name != "mac" {
		t.Errorf("Reviewer(\"\", external) = %+v %v %v", r, ok, err)
	}
	// A name is returned whatever family is asked; the caller judges the fit.
	if r, ok, err := s.Reviewer(ctx, "claude", FamilyExternal); err != nil || !ok || r.Model != "claude-haiku-5-5" {
		t.Errorf("Reviewer(claude) = %+v %v %v", r, ok, err)
	}
	if _, ok, err := s.Reviewer(ctx, "nobody", FamilyAPI); err != nil || ok {
		t.Errorf("Reviewer(nobody) = ok %v %v, want not found without error", ok, err)
	}
	if err := s.SetDefaultReviewer(ctx, "nobody"); err == nil {
		t.Error("a default that does not exist was accepted")
	}
	if err := s.DeleteReviewer(ctx, "grok-4"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Defaults(ctx); d.API != "" || d.External != "mac" {
		t.Errorf("defaults after deleting the api default = %+v, want only the external one", d)
	}
	if err := s.DeleteReviewer(ctx, "grok-4"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if err := s.ClearDefaultReviewer(ctx, FamilyExternal); err != nil {
		t.Errorf("clearing the external default: %v", err)
	}
	if d, _ := s.Defaults(ctx); d.External != "" {
		t.Errorf("external default not cleared: %+v", d)
	}
	// A default edited into the other family stops being that default.
	if err := s.SetDefaultReviewer(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutReviewer(ctx, Reviewer{Name: "mac", Kind: KindAnthropic}); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Defaults(ctx); d.External != "" || d.API != "" {
		t.Errorf("defaults after mac moved to the api family = %+v, want none", d)
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
		{Reviewer{Name: "x", Kind: KindClaudeCode, KeySecret: "SOME_KEY"}, "key_secret"},
		{Reviewer{Name: "x", Kind: KindClaudeCode, BaseURL: "https://x"}, "base_url"},
	} {
		_, err := s.PutReviewer(ctx, bad.r)
		var inv *change.InvalidError
		if !errors.As(err, &inv) || len(inv.Problems) == 0 || inv.Problems[0].Field != bad.want {
			t.Errorf("PutReviewer(%+v) = %v, want a problem on %s", bad.r, err, bad.want)
		}
	}
}

func TestLegacyAISettingsBecomeTheDefaultProfilesOnce(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	// The WU-025 settings as an upgraded server finds them: the model and
	// tools were used by the loop, the provider and URL by the container.
	for k, v := range map[string]string{keyProvider: "openai", keyBaseURL: "https://api.groq.com/openai/v1", keyModel: "llama-4", keyTools: review.ToolsFull} {
		if err := s.db.SetSetting(ctx, k, v, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.Reviewer(ctx, "", FamilyAPI)
	if err != nil || !ok {
		t.Fatalf("api default after migration: %+v %v %v", r, ok, err)
	}
	want := Reviewer{Name: "openai-api", Kind: KindOpenAI, BaseURL: "https://api.groq.com/openai/v1", Model: "llama-4", Tools: review.ToolsFull, KeySecret: "OPENAI_API_KEY"}
	r.UpdatedAt = time.Time{}
	if r != want {
		t.Errorf("migrated api profile = %+v\nwant %+v", r, want)
	}
	// The external family gets a claude-code profile with the loop's tools,
	// no model (llama-4 is not Claude Code's) and no key.
	c, ok, err := s.Reviewer(ctx, "", FamilyExternal)
	if err != nil || !ok || c.Name != "claude-code" || c.Kind != KindClaudeCode || c.Tools != review.ToolsFull || c.Model != "" || c.KeySecret != "" {
		t.Errorf("migrated claude-code profile = %+v %v %v", c, ok, err)
	}
	for _, k := range []string{keyProvider, keyBaseURL, keyModel, keyTools} {
		if _, _, set, _ := s.db.GetSetting(ctx, k); set {
			t.Errorf("legacy setting %s still present", k)
		}
	}
	// Running again, after the operator removed every profile, creates nothing.
	for _, name := range []string{"openai-api", "claude-code"} {
		if err := s.DeleteReviewer(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Reviewers(ctx); len(list) != 0 {
		t.Errorf("profiles recreated on the second start: %+v", list)
	}

	// A fresh install gets both built-in defaults.
	fresh := newService(t)
	if err := fresh.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	r, ok, _ = fresh.Reviewer(ctx, "", FamilyAPI)
	if !ok || r.Name != "claude-api" || r.Kind != KindAnthropic || r.Tools != review.ToolsReadOnly || r.KeySecret != "ANTHROPIC_API_KEY" || r.Model != "" {
		t.Errorf("fresh api default = %+v %v", r, ok)
	}
	c, ok, _ = fresh.Reviewer(ctx, "", FamilyExternal)
	if !ok || c.Name != "claude-code" || c.Kind != KindClaudeCode || c.Tools != review.ToolsReadOnly || c.KeySecret != "" {
		t.Errorf("fresh external default = %+v %v", c, ok)
	}
}

// TestAServerUpgradedFromWU028GetsAClaudeCodeProfile: an installation whose
// first migration already produced an anthropic "default" (used by the
// loop too) gains a claude-code profile with that default's model and
// tools, as the external default; the api profile is renamed claude-api.
func TestAServerUpgradedFromWU028GetsAClaudeCodeProfile(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	if _, err := s.PutReviewer(ctx, Reviewer{Name: "default", Kind: KindAnthropic, Model: "claude-opus-5-5", Tools: review.ToolsFull, KeySecret: "ANTHROPIC_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{keyReviewersMigrated: "1", "reviewers.default": "default"} {
		if err := s.db.SetSetting(ctx, k, v, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	// The api profile the first migration called "default" is renamed,
	// since there are two defaults now.
	d, _ := s.Defaults(ctx)
	if d.API != "claude-api" || d.External != "claude-code" {
		t.Errorf("defaults = %+v", d)
	}
	if _, ok, _ := s.Reviewer(ctx, "default", FamilyAPI); ok {
		t.Error("the profile called default is still there")
	}
	c, ok, _ := s.Reviewer(ctx, "", FamilyExternal)
	if !ok || c.Kind != KindClaudeCode || c.Model != "claude-opus-5-5" || c.Tools != review.ToolsFull || c.KeySecret != "" {
		t.Errorf("claude-code profile = %+v %v", c, ok)
	}
	// An api profile already called claude-code keeps its name; the new
	// external one is claude-code-2 and becomes the external default.
	clash := newService(t)
	if _, err := clash.PutReviewer(ctx, Reviewer{Name: "claude-code", Kind: KindAnthropic, KeySecret: "ANTHROPIC_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{keyReviewersMigrated: "1", "reviewers.default": "claude-code"} {
		if err := clash.db.SetSetting(ctx, k, v, clash.now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := clash.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := clash.Defaults(ctx); d.API != "claude-code" || d.External != "claude-code-2" {
		t.Errorf("defaults with a name clash = %+v", d)
	}
	if list, _ := clash.Reviewers(ctx); len(list) != 2 || list[1].Kind != KindClaudeCode {
		t.Errorf("profiles with a name clash = %+v", list)
	}

	// A third start changes nothing, even with claude-code removed.
	if err := s.DeleteReviewer(ctx, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureReviewers(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Reviewers(ctx); len(list) != 1 {
		t.Errorf("profiles after the third start = %+v", list)
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
