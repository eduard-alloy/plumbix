package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/eduard-alloy/plumbix/internal/auth"
)

type sessionHandlers struct {
	sessions *auth.Sessions
	origin   string
	authz    *authz
	log      *slog.Logger
}

// registerSessionRoutes puts the exchange on the public mux (a browser has
// no credentials yet) and everything else behind authentication. The
// revoke-all route shares its path with the exchange, so it sits on the
// public mux too, wrapped by hand.
func registerSessionRoutes(public, protected *http.ServeMux, az *authz, h *sessionHandlers) {
	public.HandleFunc("POST /api/v1/sessions", h.exchange)
	public.Handle("DELETE /api/v1/sessions", az.wrap(http.HandlerFunc(h.revokeAll)))
	protected.HandleFunc("POST /api/v1/login-links", h.createLink)
	protected.HandleFunc("GET /api/v1/session", h.status)
	protected.HandleFunc("DELETE /api/v1/sessions/current", h.signOut)
}

type sessionStatus struct {
	Status    string     `json:"status"`
	Via       string     `json:"via"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// createLink mints a one-time login link. Only the token holder may.
func (h *sessionHandlers) createLink(w http.ResponseWriter, r *http.Request) {
	if principalOf(r).via != viaToken {
		unauthorized(w)
		return
	}
	token, expires, err := h.sessions.NewLoginLink(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": h.origin + "/login?t=" + url.QueryEscape(token), "expires_at": expires})
}

// exchange turns a login link into a session cookie.
func (h *sessionHandlers) exchange(w http.ResponseWriter, r *http.Request) {
	if !h.authz.browserOK(w, r) {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	token, expires, err := h.sessions.Exchange(r.Context(), body.Token)
	switch {
	case errors.Is(err, auth.ErrLinkUsed):
		writeError(w, http.StatusUnauthorized, "link_used", "this sign-in link was already used")
		return
	case errors.Is(err, auth.ErrLinkExpired):
		writeError(w, http.StatusUnauthorized, "link_expired", "this sign-in link expired")
		return
	case errors.Is(err, auth.ErrLinkInvalid):
		writeError(w, http.StatusUnauthorized, "link_invalid", "this sign-in link is not known")
		return
	case err != nil:
		h.fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(auth.SessionTTL.Seconds())})
	writeJSON(w, http.StatusOK, sessionStatus{Status: "signed_in", Via: viaCookie, ExpiresAt: &expires})
}

// status tells a client how it is signed in.
func (h *sessionHandlers) status(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	s := sessionStatus{Status: "signed_in", Via: p.via}
	if p.via == viaCookie {
		expires, ok, err := h.sessions.Expiry(r.Context(), p.session)
		if err != nil {
			h.fail(w, err)
			return
		}
		if ok {
			s.ExpiresAt = &expires
		}
	}
	writeJSON(w, http.StatusOK, s)
}

// signOut ends the calling browser's session.
func (h *sessionHandlers) signOut(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.via != viaCookie {
		writeError(w, http.StatusBadRequest, "invalid_request", "only a browser session can sign out; use DELETE /api/v1/sessions with the token")
		return
	}
	if err := h.sessions.Revoke(r.Context(), p.session); err != nil {
		h.fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// revokeAll ends every browser session. Only the token holder may.
func (h *sessionHandlers) revokeAll(w http.ResponseWriter, r *http.Request) {
	if principalOf(r).via != viaToken {
		unauthorized(w)
		return
	}
	n, err := h.sessions.RevokeAll(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
}

func (h *sessionHandlers) fail(w http.ResponseWriter, err error) {
	h.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
