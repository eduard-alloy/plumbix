package host

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// testKey returns a fresh RSA key and its PEM.
func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// appServer fakes the two App endpoints: the installation lookup and the
// token mint, both of which must carry a JWT signed with key for app id.
type appServer struct {
	*httptest.Server
	mints atomic.Int32
}

func fakeApp(t *testing.T, key *rsa.PrivateKey, appID string) *appServer {
	t.Helper()
	s := &appServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/a/b/installation" || r.URL.Path == "/app" || r.URL.Path == "/app/installations/42/access_tokens":
			if !validJWT(t, r.Header.Get("Authorization"), &key.PublicKey, appID) {
				http.Error(w, "bad jwt", http.StatusUnauthorized)
				return
			}
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/app":
			_, _ = w.Write([]byte(`{"slug":"plumbix"}`))
		case "/repos/a/b/installation":
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			s.mints.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"inst-tok","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// validJWT checks an RS256 JWT's signature and issuer.
func validJWT(t *testing.T, header string, pub *rsa.PublicKey, iss string) bool {
	t.Helper()
	tok, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	return json.Unmarshal(payload, &claims) == nil && claims.Iss == iss && claims.Exp > time.Now().Unix()
}

// TestGitHubAppMintsInstallationToken: without a PAT, Auth signs a JWT,
// finds the installation and mints a token, reused until it nears expiry.
func TestGitHubAppMintsInstallationToken(t *testing.T) {
	key, pemKey := testKey(t)
	srv := fakeApp(t, key, "1234")
	app, err := NewGitHubApp("1234", pemKey)
	if err != nil {
		t.Fatal(err)
	}
	g := &GitHub{App: app, APIBase: srv.URL}
	a, ok := g.Auth(repo)
	if !ok || a.User != gitHubUser || a.Token != "inst-tok" {
		t.Fatalf("auth = %+v, %v (%v)", a, ok, g.App.lastErr)
	}
	if a, ok := g.Auth(repo); !ok || a.Token != "inst-tok" {
		t.Fatalf("second auth = %+v, %v", a, ok)
	}
	if n := srv.mints.Load(); n != 1 {
		t.Errorf("minted %d tokens, want one (cached)", n)
	}
	if _, ok, err := g.TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("app check: ok=%v err=%v", ok, err)
	}
}

// TestGitHubAppKeyForms: the key secret may be the PEM with literal \n
// line breaks or the PEM base64-encoded, since the secrets file holds one
// line per value.
func TestGitHubAppKeyForms(t *testing.T) {
	_, pemKey := testKey(t)
	for name, form := range map[string]string{
		"escaped": strings.ReplaceAll(pemKey, "\n", `\n`),
		"base64":  base64.StdEncoding.EncodeToString([]byte(pemKey)),
		"plain":   pemKey,
	} {
		if _, err := NewGitHubApp("1", form); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewGitHubApp("1", "not a key"); err == nil || strings.Contains(err.Error(), "not a key") {
		t.Errorf("bad key: err = %v (must not echo the value)", err)
	}
	if _, err := NewGitHubApp("", pemKey); err == nil {
		t.Error("missing app id accepted")
	}
}

// TestGitHubAuthPrefersPAT: a token wins over the App, and the App never
// talks to GitHub then.
func TestGitHubAuthPrefersPAT(t *testing.T) {
	key, pemKey := testKey(t)
	srv := fakeApp(t, key, "1234")
	app, _ := NewGitHubApp("1234", pemKey)
	g := &GitHub{Token: "pat", App: app, APIBase: srv.URL}
	if a, ok := g.Auth(repo); !ok || a.Token != "pat" {
		t.Fatalf("auth = %+v, %v", a, ok)
	}
	if n := srv.mints.Load(); n != 0 {
		t.Errorf("minted %d tokens with a PAT set", n)
	}
	r := FromSecrets(map[string]string{GitHubAppIDKey: "1234", GitHubAppKeyKey: pemKey})
	if gh := r[change.HostGitHub].(*GitHub); gh.App == nil {
		t.Error("registry built without the App from the secrets")
	}
	broken := FromSecrets(map[string]string{GitHubAppIDKey: "1234", GitHubAppKeyKey: "garbage"})[change.HostGitHub].(*GitHub)
	if broken.App != nil || broken.AppErr == nil {
		t.Error("registry kept an unusable App or lost its error")
	}
	if _, _, err := broken.TokenExpiry(context.Background()); err == nil || !strings.Contains(err.Error(), GitHubAppKeyKey) || strings.Contains(err.Error(), "garbage") {
		t.Errorf("token check with a broken App: %v", err)
	}
}

// TestGitHubAppFailureIsReported: when minting fails, Auth is anonymous
// and TokenExpiry says why, without the key in the message.
func TestGitHubAppFailureIsReported(t *testing.T) {
	key, pemKey := testKey(t)
	srv := fakeApp(t, key, "9999") // the server expects another app id
	app, _ := NewGitHubApp("1234", pemKey)
	g := &GitHub{App: app, APIBase: srv.URL}
	if _, ok := g.Auth(repo); ok {
		t.Fatal("auth succeeded against a server that refused the JWT")
	}
	_, _, err := g.TokenExpiry(context.Background())
	if err == nil || !strings.Contains(err.Error(), "App") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Errorf("err = %v", err)
	}
}

// TestGitHubAppRefreshesExpiredTokens: a cached token is replaced once the
// clock passes its expiry minus the refresh margin.
func TestGitHubAppRefreshesExpiredTokens(t *testing.T) {
	key, pemKey := testKey(t)
	srv := fakeApp(t, key, "1234")
	app, err := NewGitHubApp("1234", pemKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	app.now = func() time.Time { return now }
	g := &GitHub{App: app, APIBase: srv.URL}
	if _, ok := g.Auth(repo); !ok || srv.mints.Load() != 1 {
		t.Fatalf("first mint: ok=%v mints=%d", ok, srv.mints.Load())
	}
	now = now.Add(30 * time.Minute)
	if _, ok := g.Auth(repo); !ok || srv.mints.Load() != 1 {
		t.Fatalf("token still fresh: ok=%v mints=%d", ok, srv.mints.Load())
	}
	// The fake's tokens last an hour; a minute before that they are stale.
	now = now.Add(30*time.Minute - 30*time.Second)
	if _, ok := g.Auth(repo); !ok || srv.mints.Load() != 2 {
		t.Fatalf("token near expiry not refreshed: ok=%v mints=%d", ok, srv.mints.Load())
	}
}
