package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebIsServedBesideTheAPI: the app owns every path outside /api/, the
// API keeps its protection, and without an app the root is a plain 404.
func TestWebIsServedBesideTheAPI(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("spa:" + r.URL.Path)) })
	h := NewHandler(Deps{Version: "dev", Token: testToken, Web: spa})
	for _, path := range []string{"/", "/login", "/changes/c_1", "/assets/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "spa:"+path {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown API path without token: %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_found") {
		t.Errorf("unknown API path with token: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("health: %d", rec.Code)
	}

	plain := NewHandler(Deps{Version: "dev", Token: testToken})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/changes/c_1", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_found") {
		t.Errorf("without an app: %d %s", rec.Code, rec.Body.String())
	}
}
