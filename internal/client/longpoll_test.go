package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWaitOutlivesTheRequestTimeout: a long poll is bounded by its context,
// not by the per-request timeout that would abandon a claimed review.
func TestWaitOutlivesTheRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "t")
	c.http = &http.Client{Timeout: 20 * time.Millisecond}
	if _, err := c.Do(context.Background(), http.MethodGet, "/slow", nil); err == nil {
		t.Fatal("Do with a 20ms timeout answered an 80ms request")
	}
	resp, err := c.Wait(context.Background(), http.MethodGet, "/slow", nil)
	if err != nil || !resp.OK() {
		t.Fatalf("Wait: %v %+v", err, resp)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Wait(ctx, http.MethodGet, "/slow", nil); err == nil {
		t.Fatal("Wait outlived its context")
	}
}
