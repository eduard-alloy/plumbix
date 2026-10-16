// Package host is the narrow boundary between Plumbix and git hosts. Git
// itself does most of the work; a host only knows its credentials and how to
// check their expiry.
package host

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// Host is one git hosting service.
type Host interface {
	Kind() string
	// Auth returns credentials for the repo; false means fetch anonymously.
	Auth(repo change.Repo) (gitops.Auth, bool)
	// TokenExpiry reports when the configured token expires; false means it
	// doesn't expire or there is no token.
	TokenExpiry(ctx context.Context) (time.Time, bool, error)
}

// Registry maps host kinds to their adapters.
type Registry map[string]Host

// For returns the adapter for a repo's host.
func (r Registry) For(repo change.Repo) (Host, error) {
	h, ok := r[repo.Host]
	if !ok {
		return nil, fmt.Errorf("no adapter for host %q", repo.Host)
	}
	return h, nil
}

// repoTokenPrefix starts the secret key of a repository's own token.
const repoTokenPrefix = "PLUMBIX_REPO_TOKEN_"

// RepoTokenKey is the secret key holding one repository's own token, which
// wins over the host-wide token for that repository. Repository ids are
// lowercase; the key is their uppercase form.
func RepoTokenKey(repoID string) string {
	return repoTokenPrefix + strings.ToUpper(repoID)
}

// IsRepoTokenKey reports whether key names a repository's own token.
func IsRepoTokenKey(key string) bool {
	return strings.HasPrefix(key, repoTokenPrefix)
}

// repoTokens collects repository tokens from the secrets, by repository id.
func repoTokens(secrets map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range secrets {
		if IsRepoTokenKey(k) && v != "" {
			out[strings.ToLower(strings.TrimPrefix(k, repoTokenPrefix))] = v
		}
	}
	return out
}

// FromSecrets builds the registry from the secrets file's values.
func FromSecrets(secrets map[string]string) Registry {
	perRepo := repoTokens(secrets)
	return Registry{
		change.HostGitHub:      &GitHub{Token: secrets[GitHubTokenKey], RepoTokens: perRepo},
		change.HostAzureDevOps: &AzureDevOps{Token: secrets[AzureDevOpsTokenKey], Expires: secrets[AzureDevOpsExpiryKey], RepoTokens: perRepo},
		change.HostGit:         Plain{},
	}
}

// tokenFor picks a repository's own token when it has one.
func tokenFor(repo change.Repo, hostToken string, perRepo map[string]string) string {
	if t := perRepo[repo.ID]; t != "" {
		return t
	}
	return hostToken
}

// Plain is a git remote reached without host credentials: a local path, or
// SSH with the user's own keys.
type Plain struct{}

// Kind implements Host.
func (Plain) Kind() string { return change.HostGit }

// Auth implements Host. Plain git never sends credentials.
func (Plain) Auth(change.Repo) (gitops.Auth, bool) { return gitops.Auth{}, false }

// TokenExpiry implements Host. There is no token.
func (Plain) TokenExpiry(context.Context) (time.Time, bool, error) { return time.Time{}, false, nil }

// apiTimeout bounds calls to host APIs.
const apiTimeout = 15 * time.Second

var apiClient = &http.Client{Timeout: apiTimeout}
