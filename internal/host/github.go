package host

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
)

// GitHub settings.
const (
	GitHubTokenKey  = "PLUMBIX_GITHUB_TOKEN" // name in the secrets file
	gitHubAPI       = "https://api.github.com"
	gitHubUser      = "x-access-token"
	gitHubAPIVer    = "2022-11-28"
	gitHubExpiryHdr = "GitHub-Authentication-Token-Expiration"
	gitHubExpiryFmt = "2006-01-02 15:04:05 MST"
)

// GitHub authenticates with a personal access token.
type GitHub struct {
	Token      string
	APIBase    string            // empty means https://api.github.com
	RepoTokens map[string]string // a repository's own token, by repository id
}

// Kind implements Host.
func (g *GitHub) Kind() string { return change.HostGitHub }

// Auth implements Host.
func (g *GitHub) Auth(repo change.Repo) (gitops.Auth, bool) {
	token := tokenFor(repo, g.Token, g.RepoTokens)
	if token == "" {
		return gitops.Auth{}, false
	}
	return gitops.Auth{User: gitHubUser, Token: token}, true
}

// TokenExpiry reads the expiry GitHub reports on any authenticated call.
func (g *GitHub) TokenExpiry(ctx context.Context) (time.Time, bool, error) {
	if g.Token == "" {
		return time.Time{}, false, nil
	}
	base := g.APIBase
	if base == "" {
		base = gitHubAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return time.Time{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVer)
	resp, err := apiClient.Do(req)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("check GitHub token: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return time.Time{}, false, fmt.Errorf("GitHub rejected the token in %s", GitHubTokenKey)
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, false, fmt.Errorf("check GitHub token: status %d", resp.StatusCode)
	}
	v := resp.Header.Get(gitHubExpiryHdr)
	if v == "" {
		return time.Time{}, false, nil
	}
	exp, err := time.Parse(gitHubExpiryFmt, v)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse GitHub token expiry %q: %w", v, err)
	}
	return exp.UTC(), true, nil
}
