package settings

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/store"
)

var ctx = context.Background()

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(db)
	s.now = func() time.Time { return time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC) }
	return s
}

func TestGuidanceAndModel(t *testing.T) {
	s := newService(t)
	g, err := s.Guidance(ctx, "r_1")
	if err != nil || g.Global != "" || g.Repository != "" {
		t.Fatalf("empty guidance = %+v, %v", g, err)
	}
	if m, err := s.AIModel(ctx); err != nil || m != "" {
		t.Fatalf("empty model = %q, %v", m, err)
	}
	if err := s.SetGlobalGuidance(ctx, "  Prefer small, well-named changes.  "); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepoGuidance(ctx, "r_1", "Every exported Go function has a doc comment."); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIModel(ctx, "claude-sonnet-5-5"); err != nil {
		t.Fatal(err)
	}
	g, _ = s.Guidance(ctx, "r_1")
	if g.Global != "Prefer small, well-named changes." || g.Repository != "Every exported Go function has a doc comment." {
		t.Errorf("guidance = %+v", g)
	}
	if other, _ := s.Guidance(ctx, "r_2"); other.Repository != "" || other.Global == "" {
		t.Errorf("another repo = %+v", other)
	}
	if m, _ := s.AIModel(ctx); m != "claude-sonnet-5-5" {
		t.Errorf("model = %q", m)
	}
	// Empty clears.
	if err := s.SetRepoGuidance(ctx, "r_1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIModel(ctx, ""); err != nil {
		t.Fatal(err)
	}
	g, _ = s.Guidance(ctx, "r_1")
	if g.Repository != "" {
		t.Errorf("repo guidance not cleared: %+v", g)
	}
	if m, _ := s.AIModel(ctx); m != "" {
		t.Errorf("model not cleared: %q", m)
	}
	if err := s.SetAIModel(ctx, "bad model name!"); err == nil {
		t.Error("a model name with spaces or punctuation was accepted")
	}
	if err := s.SetGlobalGuidance(ctx, string(make([]byte, MaxGuidanceChars+1))); err == nil {
		t.Error("guidance over the limit was accepted")
	}
}

func TestSecretMetaAndKnownKeys(t *testing.T) {
	s := newService(t)
	known := KnownSecrets()
	if len(known) < 4 || known[0].Key != "PLUMBIX_GITHUB_TOKEN" || known[0].Host != "github" {
		t.Fatalf("known secrets = %+v", known)
	}
	for _, k := range known {
		if k.Purpose == "" {
			t.Errorf("%s has no purpose", k.Key)
		}
	}
	if _, ok, _ := s.SecretUpdated(ctx, "PLUMBIX_GITHUB_TOKEN"); ok {
		t.Fatal("never-set secret has a time")
	}
	if err := s.MarkSecret(ctx, "PLUMBIX_GITHUB_TOKEN"); err != nil {
		t.Fatal(err)
	}
	when, ok, err := s.SecretUpdated(ctx, "PLUMBIX_GITHUB_TOKEN")
	if err != nil || !ok || when.IsZero() {
		t.Fatalf("SecretUpdated = %v %v %v", when, ok, err)
	}
	if err := s.ForgetSecret(ctx, "PLUMBIX_GITHUB_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SecretUpdated(ctx, "PLUMBIX_GITHUB_TOKEN"); ok {
		t.Error("forgotten secret still has a time")
	}
}

func TestAIToolsDefaultsToReadOnlyAndAcceptsFull(t *testing.T) {
	s := newService(t)
	if v, err := s.AITools(ctx); err != nil || v != "read-only" {
		t.Fatalf("default tools = %q %v", v, err)
	}
	if err := s.SetAITools(ctx, "full"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.AITools(ctx); v != "full" {
		t.Errorf("tools = %q, want full", v)
	}
	if err := s.SetAITools(ctx, "everything"); err == nil {
		t.Error("an unknown tools policy was accepted")
	}
	if err := s.SetAITools(ctx, "read-only"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.AITools(ctx); v != "read-only" {
		t.Errorf("tools = %q, want read-only", v)
	}
}

func TestAIProviderAndBaseURL(t *testing.T) {
	s := newService(t)
	if p, err := s.AIProvider(ctx); err != nil || p != "anthropic" {
		t.Fatalf("default provider = %q %v", p, err)
	}
	if err := s.SetAIProvider(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.AIProvider(ctx); p != "openai" {
		t.Errorf("provider = %q", p)
	}
	if err := s.SetAIProvider(ctx, "bedrock"); err == nil {
		t.Error("an unknown provider was accepted")
	}
	if u, _ := s.AIBaseURL(ctx); u != "" {
		t.Errorf("default base URL = %q", u)
	}
	if err := s.SetAIBaseURL(ctx, "https://api.openai.com/v1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.AIBaseURL(ctx); u != "https://api.openai.com/v1" {
		t.Errorf("base URL = %q", u)
	}
	for _, bad := range []string{"api.openai.com/v1", "ftp://x", "https://", "https://a b"} {
		if err := s.SetAIBaseURL(ctx, bad); err == nil {
			t.Errorf("base URL %q was accepted", bad)
		}
	}
	if err := s.SetAIBaseURL(ctx, "http://localhost:11434/v1"); err != nil {
		t.Errorf("a local http endpoint was refused: %v", err)
	}
	if err := s.SetAIBaseURL(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.AIBaseURL(ctx); u != "" {
		t.Errorf("cleared base URL = %q", u)
	}
	if err := s.SetAIProvider(ctx, "anthropic"); err != nil {
		t.Fatal(err)
	}
}
