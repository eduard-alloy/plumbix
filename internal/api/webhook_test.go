package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGitHubWebhookRouteNeedsNoToken: the webhook route is served outside
// the bearer-token check (it verifies GitHub's signature itself) and is
// absent when no handler is wired.
func TestGitHubWebhookRouteNeedsNoToken(t *testing.T) {
	seen := false
	h := NewHandler(Deps{Version: "dev", Token: testToken, GitHubWebhook: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		w.WriteHeader(http.StatusAccepted)
	})})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/github", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || !seen {
		t.Fatalf("webhook without a token: %d (reached: %v)", rec.Code, seen)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/hooks/github", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET on the webhook path without a token: %d, want the token check", rec.Code)
	}
	none := NewHandler(Deps{Version: "dev", Token: testToken})
	rec = httptest.NewRecorder()
	none.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hooks/github", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unwired webhook: %d, want the token check", rec.Code)
	}
}
