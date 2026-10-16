package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerServesFilesAndFallsBackToIndex(t *testing.T) {
	h := Handler(fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Plumbix</title>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"fonts/x.woff2":      {Data: []byte("woff")},
	})
	for _, path := range []string{"/", "/login?t=abc", "/changes/c_k7m2q9x4w1zp", "/nope/deeper"} {
		rec := get(h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Plumbix") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q %s", path, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: index headers = %v", path, rec.Header())
		}
	}
	js := get(h, "/assets/app-1a2b.js")
	if js.Code != http.StatusOK || js.Body.String() != "console.log(1)" || !strings.Contains(js.Header().Get("Content-Type"), "javascript") || !strings.Contains(js.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %v", js.Code, js.Body.String(), js.Header())
	}
	if font := get(h, "/fonts/x.woff2"); font.Code != http.StatusOK || !strings.Contains(font.Header().Get("Content-Type"), "font/woff2") {
		t.Errorf("font: %d %v", font.Code, font.Header())
	}
	if rec := get(h, "/assets/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Errorf("a directory path falls back to the app, got %d %q", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to the app: %d", rec.Code)
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {}})
	rec := get(h, "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("unbuilt app: %d %q", rec.Code, rec.Body.String())
	}
}

func TestDistIsEmbedded(t *testing.T) {
	// The embedded directory always exists; whether index.html is in it
	// depends on whether make web ran. Either way Dist must be usable.
	if _, err := Dist().Open("."); err != nil {
		t.Fatal(err)
	}
}
