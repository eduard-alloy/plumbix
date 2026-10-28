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

// GitHub authenticates with a personal access token, or as a GitHub App
// when no token applies (WU-054).
type GitHub struct {
	Token      string
	APIBase    string            // empty means https://api.github.com
	RepoTokens map[string]string // a repository's own token, by repository id
	App        *GitHubApp        // nil when no App is configured
	// AppErr says why the configured App is unusable (a malformed key or a
	// missing id); reported by TokenExpiry when no token applies.
	AppErr error
}

// Kind implements Host.
func (g *GitHub) Kind() string { return change.HostGitHub }

// Auth implements Host: the repository's own token, else the host token,
// else an installation token of the App. A failed mint leaves the fetch
// anonymous and is reported by TokenExpiry.
func (g *GitHub) Auth(repo change.Repo) (gitops.Auth, bool) {
	token := tokenFor(repo, g.Token, g.RepoTokens)
	if token == "" && g.App != nil {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		token, _ = g.apiToken(ctx, repo)
	}
	if token == "" {
		return gitops.Auth{}, false
	}
	return gitops.Auth{User: gitHubUser, Token: token}, true
}

// AuthWith is git credentials for token, or the host token when it is
// empty: what a push to a repository not yet registered uses (WU-042).
func (g *GitHub) AuthWith(token string) (gitops.Auth, bool) {
	if token == "" {
		token = g.Token
	}
	return gitops.Auth{User: gitHubUser, Token: token}, token != ""
}

// TokenExpiry reads the expiry GitHub reports on any authenticated call.
// Without a token it checks the App instead, whose installation tokens
// renew themselves: it reports no expiry, or why the last mint failed.
func (g *GitHub) TokenExpiry(ctx context.Context) (time.Time, bool, error) {
	if g.Token == "" {
		if g.AppErr != nil {
			return time.Time{}, false, fmt.Errorf("GitHub App: %w", g.AppErr)
		}
		if g.App == nil {
			return time.Time{}, false, nil
		}
		if _, err := g.App.Check(ctx, g.base()); err != nil {
			return time.Time{}, false, err
		}
		g.App.mu.Lock()
		err := g.App.lastErr
		g.App.mu.Unlock()
		if err != nil {
			return time.Time{}, false, fmt.Errorf("GitHub App: %w", err)
		}
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
