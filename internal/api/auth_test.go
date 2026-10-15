package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func request(t *testing.T, method, path, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("version: 1\nsteps:\n  - {name: a, image: x, run: x}\n"))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	NewHandler("dev", testToken).ServeHTTP(rec, req)
	return rec
}

func TestProtectedRouteRejectsBadTokens(t *testing.T) {
	for _, header := range []string{
		"",
		"Bearer wrong",
		"Bearer " + testToken[:63],
		"Bearer " + testToken + "x",
		"Basic " + testToken,
		testToken,
		"bearer" + testToken,
	} {
		rec := request(t, http.MethodPost, "/api/v1/pipelines/check", header)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: status = %d, want 401", header, rec.Code)
			continue
		}
		if rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("header %q: WWW-Authenticate = %q", header, rec.Header().Get("WWW-Authenticate"))
		}
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "unauthorized" {
			t.Errorf("header %q: body = %s", header, rec.Body.String())
		}
	}
}

func TestProtectedRouteAcceptsToken(t *testing.T) {
	rec := request(t, http.MethodPost, "/api/v1/pipelines/check", "Bearer "+testToken)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	if rec := request(t, http.MethodGet, "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestUnknownRouteHiddenWithoutToken(t *testing.T) {
	if rec := request(t, http.MethodGet, "/api/v1/nope", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without token: status = %d, want 401", rec.Code)
	}
	if rec := request(t, http.MethodGet, "/api/v1/nope", "Bearer "+testToken); rec.Code != http.StatusNotFound {
		t.Errorf("with token: status = %d, want 404", rec.Code)
	}
}

func TestEmptyServerTokenRejectsEverything(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/check", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer ")
	NewHandler("dev", "").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the server has no token", rec.Code)
	}
}
