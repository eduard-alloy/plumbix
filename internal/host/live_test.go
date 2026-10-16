package host

import (
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// TestLiveSourceReflectsNewSecrets: a token that appears later is used by
// the next lookup, so settings changes need no restart.
func TestLiveSourceReflectsNewSecrets(t *testing.T) {
	values := map[string]string{}
	src := Live(func() map[string]string { return values })
	repo := change.Repo{Host: change.HostGitHub}
	h, err := src.Hosts().For(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Auth(repo); ok {
		t.Fatal("credentials without a token")
	}
	values[GitHubTokenKey] = "ghp_new"
	h, _ = src.Hosts().For(repo)
	auth, ok := h.Auth(repo)
	if !ok || auth.Token != "ghp_new" {
		t.Fatalf("auth after the token appeared = %+v, %v", auth, ok)
	}
	// A plain Registry is a Source too, for callers that never change.
	var fixed Source = Registry{change.HostGitHub: &GitHub{Token: "fixed"}}
	if h, _ := fixed.Hosts().For(repo); h == nil {
		t.Fatal("registry as source")
	}
}
