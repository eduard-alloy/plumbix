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

// TestRepositoryTokenOverridesTheHostToken: a fine-grained token covers
// the repositories chosen for it, so a repository may carry its own token;
// it wins over the host-wide one, and other repositories keep the default.
func TestRepositoryTokenOverridesTheHostToken(t *testing.T) {
	secrets := map[string]string{
		GitHubTokenKey:                 "host-wide",
		RepoTokenKey("r_abc123def456"): "just-this-repo",
		RepoTokenKey("r_azure0000001"): "azure-repo",
	}
	reg := FromSecrets(secrets)
	gh, _ := reg.For(change.Repo{Host: change.HostGitHub})
	if a, ok := gh.Auth(change.Repo{ID: "r_abc123def456", Host: change.HostGitHub}); !ok || a.Token != "just-this-repo" || a.User != "x-access-token" {
		t.Errorf("repo with its own token: %+v %v", a, ok)
	}
	if a, ok := gh.Auth(change.Repo{ID: "r_other0000000", Host: change.HostGitHub}); !ok || a.Token != "host-wide" {
		t.Errorf("repo without its own token: %+v %v", a, ok)
	}
	ado, _ := reg.For(change.Repo{Host: change.HostAzureDevOps})
	if a, ok := ado.Auth(change.Repo{ID: "r_azure0000001", Host: change.HostAzureDevOps}); !ok || a.Token != "azure-repo" {
		t.Errorf("azure repo token without a host token: %+v %v", a, ok)
	}
	if _, ok := ado.Auth(change.Repo{ID: "r_other0000000", Host: change.HostAzureDevOps}); ok {
		t.Error("azure repo without any token got credentials")
	}
	if RepoTokenKey("r_abc123def456") != "PLUMBIX_REPO_TOKEN_R_ABC123DEF456" || !IsRepoTokenKey(RepoTokenKey("r_x")) || IsRepoTokenKey(GitHubTokenKey) {
		t.Errorf("key shape: %s", RepoTokenKey("r_abc123def456"))
	}
}
