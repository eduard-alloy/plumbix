package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler("1.2.3", testToken).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["status"] != "ok" || body["version"] != "1.2.3" {
		t.Errorf("body = %v, want status ok and version 1.2.3", body)
	}
}

func TestUnknownRouteReturnsErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	NewHandler("dev", testToken).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Error.Code != "not_found" || body.Error.Message == "" {
		t.Errorf("error = %+v, want code not_found with a message", body.Error)
	}
}
