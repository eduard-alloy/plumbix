package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/auth"
)

// sessionCookie carries a browser's session token.
const sessionCookie = "plumbix_session"

// How a request authenticated.
const (
	viaToken  = "token"
	viaCookie = "cookie"
)

type principalKey struct{}

// principal is who made an authenticated request.
type principal struct {
	via     string
	session string // the session token, for cookie requests
}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// authz decides who may call the API: the holder of the local bearer
// token, or a browser with a session cookie that also passes the browser
// checks. The response never says whether a token was missing or wrong.
type authz struct {
	token    []byte
	sessions *auth.Sessions // nil disables cookie sign-in
	host     string         // the Host browsers must use, from Deps.Origin
}

func newAuthz(token string, sessions *auth.Sessions, origin string) *authz {
	a := &authz{token: []byte(token), sessions: sessions}
	if u, err := url.Parse(origin); err == nil {
		a.host = u.Host
	}
	return a
}

// wrap requires authentication for next.
func (a *authz) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.bearerOK(r) {
			next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), principal{via: viaToken})))
			return
		}
		if a.sessions != nil {
			if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
				if !a.browserOK(w, r) {
					return
				}
				ok, err := a.sessions.Valid(r.Context(), c.Value)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal", "internal error")
					return
				}
				if ok {
					next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), principal{via: viaCookie, session: c.Value})))
					return
				}
			}
		}
		unauthorized(w)
	})
}

func (a *authz) bearerOK(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && len(a.token) > 0 && subtle.ConstantTimeCompare([]byte(got), a.token) == 1
}

// browserOK applies the checks every browser request must pass: the Host
// is the server's own address (a DNS-rebinding page would carry another),
// and a state-changing request names that same address as its Origin (a
// cross-site page cannot). On failure it writes the response.
func (a *authz) browserOK(w http.ResponseWriter, r *http.Request) bool {
	if a.host == "" || r.Host != a.host {
		unauthorized(w)
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get("Origin") != "http://"+a.host {
		writeError(w, http.StatusForbidden, "forbidden", "cross-site request refused")
		return false
	}
	return true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized", "a valid API token or browser session is required")
}

// requireToken keeps the token-only middleware for handlers built without
// sessions.
func requireToken(token string, next http.Handler) http.Handler {
	return newAuthz(token, nil, "").wrap(next)
}
