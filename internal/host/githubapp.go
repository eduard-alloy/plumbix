package host

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHub App settings (WU-054). The App is a second identity beside the
// personal access token: it signs a JWT with its private key and mints a
// short-lived installation token per repository.
const (
	GitHubAppIDKey         = "PLUMBIX_GITHUB_APP_ID"         // the App's numeric id
	GitHubAppKeyKey        = "PLUMBIX_GITHUB_APP_KEY"        // the App's private key, PEM
	GitHubWebhookSecretKey = "PLUMBIX_GITHUB_WEBHOOK_SECRET" // verifies webhook deliveries
)

// Installation tokens last an hour; one is replaced this long before it
// expires so a request in flight never carries a dead token.
const tokenRefreshMargin = time.Minute

// jwtLifetime is how long an App JWT is valid; GitHub allows ten minutes.
const jwtLifetime = 9 * time.Minute

// GitHubApp authenticates as a GitHub App installation.
type GitHubApp struct {
	id  string
	key *rsa.PrivateKey
	mu  sync.Mutex
	// tokens caches one installation token per repository (owner/name).
	tokens map[string]installationToken
	// lastErr is the last minting failure, reported by TokenExpiry so an
	// anonymous fetch has a visible cause.
	lastErr error
	now     func() time.Time
}

type installationToken struct {
	token   string
	expires time.Time
}

// NewGitHubApp reads the App id and private key. The key is a PEM block in
// PKCS#1 or PKCS#8 form, given as is, with literal \n for its line breaks
// (the secrets file holds one line per value), or base64-encoded. The
// error never echoes the key.
func NewGitHubApp(id, key string) (*GitHubApp, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%s is required with %s", GitHubAppIDKey, GitHubAppKeyKey)
	}
	parsed, err := parsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", GitHubAppKeyKey, err)
	}
	return &GitHubApp{id: id, key: parsed, tokens: map[string]installationToken{}, now: time.Now}, nil
}

func parsePrivateKey(key string) (*rsa.PrivateKey, error) {
	key = strings.TrimSpace(key)
	if !strings.Contains(key, "-----BEGIN") {
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			return nil, errors.New("not a PEM private key, nor one base64-encoded")
		}
		key = string(raw)
	}
	key = strings.ReplaceAll(key, `\n`, "\n")
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the PEM block is not an RSA private key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not RSA; GitHub Apps use RSA keys")
	}
	return rk, nil
}

// jwt signs the App's JWT: issued a minute ago for clock skew, valid nine
// minutes, issuer the App id.
func (a *GitHubApp) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(jwtLifetime).Unix(), "iss": a.id})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign App JWT: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// InstallationToken returns a token for the App's installation on the
// repository (owner/name), minting one when none is cached or the cached
// one is about to expire.
func (a *GitHubApp) InstallationToken(ctx context.Context, base, ownerRepo string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[ownerRepo]; ok && a.now().Before(t.expires.Add(-tokenRefreshMargin)) {
		return t.token, nil
	}
	t, err := a.mint(ctx, base, ownerRepo)
	if err != nil {
		a.lastErr = err
		return "", err
	}
	a.lastErr = nil
	a.tokens[ownerRepo] = t
	return t.token, nil
}

func (a *GitHubApp) mint(ctx context.Context, base, ownerRepo string) (installationToken, error) {
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, base+"/repos/"+ownerRepo+"/installation", &inst); err != nil {
		return installationToken{}, fmt.Errorf("find the App installation for %s: %w", ownerRepo, err)
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", base, inst.ID), &tok); err != nil {
		return installationToken{}, fmt.Errorf("mint an installation token for %s: %w", ownerRepo, err)
	}
	if tok.Token == "" {
		return installationToken{}, fmt.Errorf("mint an installation token for %s: GitHub returned none", ownerRepo)
	}
	return installationToken{token: tok.Token, expires: tok.ExpiresAt}, nil
}

// Check verifies the App id and key against GitHub by reading the App,
// and returns the App's slug, the name GitHub shows it under (WU-061).
func (a *GitHubApp) Check(ctx context.Context, base string) (string, error) {
	var app struct {
		Slug string `json:"slug"`
	}
	if err := a.call(ctx, http.MethodGet, base+"/app", &app); err != nil {
		return "", fmt.Errorf("GitHub App check: %w", err)
	}
	return app.Slug, nil
}

// errNotInstalled is call's answer to a 404: the App has no installation
// on the repository asked about.
var errNotInstalled = errors.New("not found: the App is not installed on the repository")

// Installed reports whether the App is installed on the repository
// (owner/name), asking as the App itself; no token is minted (WU-061).
func (a *GitHubApp) Installed(ctx context.Context, base, ownerRepo string) (bool, error) {
	var inst struct {
		ID int64 `json:"id"`
	}
	err := a.call(ctx, http.MethodGet, base+"/repos/"+ownerRepo+"/installation", &inst)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errNotInstalled):
		return false, nil
	}
	return false, fmt.Errorf("App installation lookup for %s: %w", ownerRepo, err)
}

// call makes one JWT-authenticated request and decodes a JSON object.
func (a *GitHubApp) call(ctx context.Context, method, url string, out any) error {
	tok, err := a.jwt()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVer)
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub rejected the App's JWT (check %s and %s)", GitHubAppIDKey, GitHubAppKeyKey)
	case http.StatusNotFound:
		return errNotInstalled
	default:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub's answer: %w", err)
	}
	return nil
}
