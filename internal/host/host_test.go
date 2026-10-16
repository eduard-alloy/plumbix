package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

var repo = change.Repo{ID: "r_1", Host: change.HostGitHub, URL: "https://github.com/a/b"}

func TestGitHubAuth(t *testing.T) {
	a, ok := (&GitHub{Token: "tok"}).Auth(repo)
	if !ok || a.User != "x-access-token" || a.Token != "tok" {
		t.Errorf("auth = %+v, %v", a, ok)
	}
	if _, ok := (&GitHub{}).Auth(repo); ok {
		t.Error("auth without a token should be anonymous")
	}
}

func fakeGitHub(t *testing.T, status int, expiry string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-GitHub-Api-Version") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if expiry != "" {
			w.Header().Set("GitHub-Authentication-Token-Expiration", expiry)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"login": "someone"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubTokenExpiry(t *testing.T) {
	srv := fakeGitHub(t, http.StatusOK, "2026-11-01 12:00:00 UTC")
	exp, ok, err := (&GitHub{Token: "tok", APIBase: srv.URL}).TokenExpiry(context.Background())
	if err != nil || !ok || !exp.Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v, %v, %v", exp, ok, err)
	}
	noExpiry := fakeGitHub(t, http.StatusOK, "")
	if _, ok, err := (&GitHub{Token: "tok", APIBase: noExpiry.URL}).TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("token without expiry: ok=%v err=%v", ok, err)
	}
	if _, ok, err := (&GitHub{}).TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("no token: ok=%v err=%v", ok, err)
	}
}

func TestGitHubRejectedToken(t *testing.T) {
	srv := fakeGitHub(t, http.StatusUnauthorized, "")
	_, _, err := (&GitHub{Token: "tok", APIBase: srv.URL}).TokenExpiry(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), "tok\"") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistry(t *testing.T) {
	r := FromSecrets(map[string]string{GitHubTokenKey: "tok"})
	h, err := r.For(repo)
	if err != nil || h.Kind() != change.HostGitHub {
		t.Fatalf("host = %v, %v", h, err)
	}
	if _, ok := h.Auth(repo); !ok {
		t.Error("token from secrets not used")
	}
	if _, err := r.For(change.Repo{Host: "gitlab"}); err == nil {
		t.Error("unknown host accepted")
	}
}

func TestPlainGit(t *testing.T) {
	r := FromSecrets(map[string]string{GitHubTokenKey: "tok"})
	h, err := r.For(change.Repo{Host: change.HostGit, URL: "/srv/app.git"})
	if err != nil || h.Kind() != change.HostGit {
		t.Fatalf("host = %v, %v", h, err)
	}
	if _, ok := h.Auth(change.Repo{Host: change.HostGit}); ok {
		t.Error("plain git should never send credentials")
	}
	if _, ok, err := h.TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("expiry: ok=%v err=%v", ok, err)
	}
}

var azRepo = change.Repo{ID: "r_2", Host: change.HostAzureDevOps, URL: "https://dev.azure.com/org/proj/_git/app"}

func TestAzureDevOpsAuth(t *testing.T) {
	a, ok := (&AzureDevOps{Token: "tok"}).Auth(azRepo)
	if !ok || a.User == "" || a.Token != "tok" {
		t.Errorf("auth = %+v, %v", a, ok)
	}
	if _, ok := (&AzureDevOps{}).Auth(azRepo); ok {
		t.Error("auth without a token should be anonymous")
	}
}

func TestAzureDevOpsTokenExpiry(t *testing.T) {
	exp, ok, err := (&AzureDevOps{Token: "tok", Expires: "2026-12-31"}).TokenExpiry(context.Background())
	if err != nil || !ok || !exp.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v, %v, %v", exp, ok, err)
	}
	if _, ok, err := (&AzureDevOps{Token: "tok"}).TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("no date: ok=%v err=%v", ok, err)
	}
	if _, ok, err := (&AzureDevOps{Expires: "2026-12-31"}).TokenExpiry(context.Background()); ok || err != nil {
		t.Errorf("no token: ok=%v err=%v", ok, err)
	}
	_, _, err = (&AzureDevOps{Token: "tok", Expires: "31/12/2026"}).TokenExpiry(context.Background())
	if err == nil || !strings.Contains(err.Error(), AzureDevOpsExpiryKey) || strings.Contains(err.Error(), "tok") {
		t.Errorf("bad date: err = %v", err)
	}
}

func TestAzureDevOpsRegistry(t *testing.T) {
	r := FromSecrets(map[string]string{AzureDevOpsTokenKey: "tok", AzureDevOpsExpiryKey: "2027-01-15"})
	h, err := r.For(azRepo)
	if err != nil || h.Kind() != change.HostAzureDevOps {
		t.Fatalf("host = %v, %v", h, err)
	}
	if a, ok := h.Auth(azRepo); !ok || a.Token != "tok" {
		t.Error("token from secrets not used")
	}
	if exp, ok, err := h.TokenExpiry(context.Background()); err != nil || !ok || exp.Year() != 2027 {
		t.Errorf("expiry from secrets: %v, %v, %v", exp, ok, err)
	}
}
