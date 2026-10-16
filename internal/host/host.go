// Package host is the narrow boundary between Plumbix and git hosts. Git
// itself does most of the work; a host only knows its credentials and how to
// check their expiry.
package host

import (
	"context"
	"fmt"
	"net/http"
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

// FromSecrets builds the registry from the secrets file's values.
func FromSecrets(secrets map[string]string) Registry {
	return Registry{
		change.HostGitHub:      &GitHub{Token: secrets[GitHubTokenKey]},
		change.HostAzureDevOps: &AzureDevOps{Token: secrets[AzureDevOpsTokenKey], Expires: secrets[AzureDevOpsExpiryKey]},
		change.HostGit:         Plain{},
	}
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
