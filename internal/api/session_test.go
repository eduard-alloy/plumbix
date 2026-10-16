package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/store"
)

const testHost = "127.0.0.1:6400"

// browserAPI is a handler with browser sign-in enabled and a clock the
// test controls.
type browserAPI struct {
	t   *testing.T
	h   http.Handler
	now time.Time
}

func newBrowserAPI(t *testing.T) *browserAPI {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := &browserAPI{t: t, now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	sessions := auth.NewSessions(db).WithClock(func() time.Time { return b.now })
	b.h = NewHandler(Deps{Version: "dev", Token: testToken, Changes: change.NewService(db), Sessions: sessions, Origin: "http://" + testHost})
	return b
}

// browserRequest is a request as a browser would send it: Host set, an
// Origin on state changes, and the session cookie when given.
type browserRequest struct {
	method, path, body string
	host, origin       string
	cookie             string
	bearer             bool
}

func (b *browserAPI) send(r browserRequest) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(r.method, "http://"+testHost+r.path, strings.NewReader(r.body))
	req.Host = testHost
	if r.host != "" {
		req.Host = r.host
	}
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: r.cookie})
	}
	if r.bearer {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	return rec
}

// signIn runs the whole flow and returns the session cookie value.
func (b *browserAPI) signIn() (link string, cookie string) {
	b.t.Helper()
	rec := b.send(browserRequest{method: http.MethodPost, path: "/api/v1/login-links", bearer: true, host: "whatever:1"})
	if rec.Code != http.StatusCreated {
		b.t.Fatalf("login-links: %d %s", rec.Code, rec.Body.String())
	}
	created := decode[struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}](b.t, rec)
	if !strings.HasPrefix(created.URL, "http://"+testHost+"/login?t=") || !created.ExpiresAt.Equal(b.now.Add(auth.LinkTTL)) {
		b.t.Fatalf("login link = %+v", created)
	}
	u, _ := url.Parse(created.URL)
	link = u.Query().Get("t")
	rec = b.send(browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "` + link + `"}`, origin: "http://" + testHost})
	if rec.Code != http.StatusOK {
		b.t.Fatalf("sessions: %d %s", rec.Code, rec.Body.String())
	}
	status := decode[struct {
		Status    string    `json:"status"`
		ExpiresAt time.Time `json:"expires_at"`
	}](b.t, rec)
	if status.Status != "signed_in" || !status.ExpiresAt.Equal(b.now.Add(auth.SessionTTL)) {
		b.t.Fatalf("session status = %+v", status)
	}
	var set *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			set = c
		}
	}
	if set == nil || !set.HttpOnly || set.SameSite != http.SameSiteStrictMode || set.Path != "/" || set.MaxAge != int(auth.SessionTTL.Seconds()) || set.Value == "" {
		b.t.Fatalf("session cookie = %+v", set)
	}
	return link, set.Value
}

func TestBrowserSignInFlow(t *testing.T) {
	o := loadOpenAPI(t)
	b := newBrowserAPI(t)
	link, cookie := b.signIn()

	// Signed in: reads and the session endpoint work with the cookie alone.
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie}); rec.Code != http.StatusOK {
		t.Errorf("GET with cookie: %d %s", rec.Code, rec.Body.String())
	}
	rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/session", cookie: cookie})
	o.check("SessionStatus", rec)
	if s := decode[struct{ Status, Via string }](t, rec); rec.Code != http.StatusOK || s.Status != "signed_in" || s.Via != "cookie" {
		t.Errorf("session: %d %s", rec.Code, rec.Body.String())
	}
	rec = b.send(browserRequest{method: http.MethodGet, path: "/api/v1/session", bearer: true})
	if s := decode[struct{ Via string }](t, rec); rec.Code != http.StatusOK || s.Via != "token" {
		t.Errorf("session with token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/session"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("session without anything: %d", rec.Code)
	}

	// The link works once; the failure names the reason.
	rec = b.send(browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "` + link + `"}`, origin: "http://" + testHost})
	o.check("Error", rec)
	if rec.Code != http.StatusUnauthorized || decode[apiError](t, rec).Error.Code != "link_used" {
		t.Errorf("second exchange: %d %s", rec.Code, rec.Body.String())
	}
	rec = b.send(browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "nope"}`, origin: "http://" + testHost})
	if rec.Code != http.StatusUnauthorized || decode[apiError](t, rec).Error.Code != "link_invalid" {
		t.Errorf("unknown link: %d %s", rec.Code, rec.Body.String())
	}
	rec = b.send(browserRequest{method: http.MethodPost, path: "/api/v1/login-links", bearer: true})
	fresh, _ := url.Parse(decode[struct{ URL string }](t, rec).URL)
	b.now = b.now.Add(auth.LinkTTL + time.Second)
	rec = b.send(browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "` + fresh.Query().Get("t") + `"}`, origin: "http://" + testHost})
	if rec.Code != http.StatusUnauthorized || decode[apiError](t, rec).Error.Code != "link_expired" {
		t.Errorf("expired link: %d %s", rec.Code, rec.Body.String())
	}
	if rec := b.send(browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": 1}`, origin: "http://" + testHost}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", rec.Code)
	}

	// The session outlives the link but not SessionTTL.
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie}); rec.Code != http.StatusOK {
		t.Errorf("GET after link expiry: %d", rec.Code)
	}
	b.now = b.now.Add(auth.SessionTTL)
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie}); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET after session expiry: %d", rec.Code)
	}
}

func TestBrowserChecksHostAndOrigin(t *testing.T) {
	b := newBrowserAPI(t)
	_, cookie := b.signIn()
	cases := []struct {
		name string
		r    browserRequest
		want int
	}{
		{"read, right host", browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie}, http.StatusOK},
		{"read, foreign host (DNS rebinding)", browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie, host: "evil.example:6400"}, http.StatusUnauthorized},
		{"read, localhost is not the listen address", browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: cookie, host: "localhost:6400"}, http.StatusUnauthorized},
		{"write, matching origin", browserRequest{method: http.MethodPost, path: "/api/v1/changes/c_nope/close", cookie: cookie, origin: "http://" + testHost}, http.StatusNotFound},
		{"write, no origin", browserRequest{method: http.MethodPost, path: "/api/v1/changes/c_nope/close", cookie: cookie}, http.StatusForbidden},
		{"write, foreign origin", browserRequest{method: http.MethodPost, path: "/api/v1/changes/c_nope/close", cookie: cookie, origin: "http://evil.example"}, http.StatusForbidden},
		{"write, origin with a different port", browserRequest{method: http.MethodPost, path: "/api/v1/changes/c_nope/close", cookie: cookie, origin: "http://127.0.0.1:6401"}, http.StatusForbidden},
		{"exchange from a foreign host", browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "x"}`, host: "evil.example:6400", origin: "http://evil.example:6400"}, http.StatusUnauthorized},
		{"exchange without origin", browserRequest{method: http.MethodPost, path: "/api/v1/sessions", body: `{"token": "x"}`}, http.StatusForbidden},
		{"bearer ignores host and origin", browserRequest{method: http.MethodPost, path: "/api/v1/changes/c_nope/close", bearer: true, host: "evil.example:6400", origin: "http://evil.example"}, http.StatusNotFound},
		{"no cookie, no token", browserRequest{method: http.MethodGet, path: "/api/v1/changes"}, http.StatusUnauthorized},
		{"garbage cookie", browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: "not-a-session"}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if rec := b.send(c.r); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func TestSignOut(t *testing.T) {
	o := loadOpenAPI(t)
	b := newBrowserAPI(t)
	_, first := b.signIn()
	_, second := b.signIn()

	rec := b.send(browserRequest{method: http.MethodDelete, path: "/api/v1/sessions/current", cookie: first, origin: "http://" + testHost})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d %s", rec.Code, rec.Body.String())
	}
	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cleared = c
		}
	}
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("sign-out cookie = %+v; want it cleared", cleared)
	}
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: first}); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed-out cookie still works: %d", rec.Code)
	}
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: second}); rec.Code != http.StatusOK {
		t.Errorf("another browser's session was ended: %d", rec.Code)
	}
	if rec := b.send(browserRequest{method: http.MethodDelete, path: "/api/v1/sessions/current", cookie: first, origin: "http://" + testHost}); rec.Code != http.StatusUnauthorized {
		t.Errorf("sign out twice: %d", rec.Code)
	}

	// plumbix logout: the token revokes every browser.
	rec = b.send(browserRequest{method: http.MethodDelete, path: "/api/v1/sessions", bearer: true})
	o.check("Revoked", rec)
	if rec.Code != http.StatusOK || decode[struct{ Revoked int }](t, rec).Revoked != 1 {
		t.Errorf("revoke all: %d %s", rec.Code, rec.Body.String())
	}
	if rec := b.send(browserRequest{method: http.MethodGet, path: "/api/v1/changes", cookie: second}); rec.Code != http.StatusUnauthorized {
		t.Errorf("session survived revoke all: %d", rec.Code)
	}
	if rec := b.send(browserRequest{method: http.MethodDelete, path: "/api/v1/sessions", cookie: second, origin: "http://" + testHost}); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoke all needs the token, got %d", rec.Code)
	}
}

func TestLoginLinkNeedsTokenAndSessionsEnabled(t *testing.T) {
	b := newBrowserAPI(t)
	if rec := b.send(browserRequest{method: http.MethodPost, path: "/api/v1/login-links"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("login-links without token: %d", rec.Code)
	}
	_, cookie := b.signIn()
	if rec := b.send(browserRequest{method: http.MethodPost, path: "/api/v1/login-links", cookie: cookie, origin: "http://" + testHost}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a browser must not mint login links: %d", rec.Code)
	}
	plain := newTestAPI(t)
	if rec := plain.do(http.MethodPost, "/api/v1/login-links", ""); rec.Code != http.StatusNotFound {
		t.Errorf("without Sessions the route must not exist: %d", rec.Code)
	}
}
