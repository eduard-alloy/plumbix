package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
	"github.com/eduard-alloy/plumbix/internal/store"
)

// fakeGitHubForReport answers what the repository GitHub report asks: the
// App itself, the installation lookup, a token mint, a branch's rules and
// its classic protection. The toggles pick each answer.
type fakeGitHubForReport struct {
	*httptest.Server
	installed  atomic.Bool
	gateInRule atomic.Bool
	protection atomic.Int32 // the status of the classic protection read
}

func newFakeGitHubForReport(t *testing.T) *fakeGitHubForReport {
	t.Helper()
	f := &fakeGitHubForReport{}
	f.installed.Store(true)
	f.gateInRule.Store(true)
	f.protection.Store(http.StatusNotFound)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/app":
			_, _ = w.Write([]byte(`{"slug":"plumbix-gate"}`))
		case "/repos/a/b/installation":
			if !f.installed.Load() {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":42}`))
		case "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"inst-tok","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case "/repos/a/b/rules/branches/main":
			if f.gateInRule.Load() {
				_, _ = w.Write([]byte(`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"plumbix/gate","integration_id":5512773}]}}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case "/repos/a/b/branches/main/protection/required_status_checks":
			http.Error(w, `{"message":"no"}`, int(f.protection.Load()))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

type gitHubReportBody struct {
	App struct {
		Configured bool   `json:"configured"`
		Slug       string `json:"slug"`
		Installed  bool   `json:"installed"`
		InstallURL string `json:"install_url"`
		Message    string `json:"message"`
	} `json:"app"`
	Gate []struct {
		Branch   string `json:"branch"`
		Required bool   `json:"required"`
		Known    bool   `json:"known"`
		Message  string `json:"message"`
	} `json:"gate"`
	Intake struct {
		Mode             string     `json:"mode"`
		WebhookSecretSet bool       `json:"webhook_secret_set"`
		LastDeliveryAt   *time.Time `json:"last_delivery_at"`
		PollInterval     string     `json:"poll_interval"`
		Message          string     `json:"message"`
	} `json:"intake"`
}

// TestRepoGitHubReport: GET /repos/{id}/github says, live and as the App,
// whether the App is installed on the repository, per protected branch
// whether plumbix/gate is required, and how pull requests reach the server
// (WU-061). A repository on another host has no report.
func TestRepoGitHubReport(t *testing.T) {
	o := loadOpenAPI(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	gh := newFakeGitHubForReport(t)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sec, err := secrets.Open(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := host.NewGitHubApp("1234", pemKey)
	if err != nil {
		t.Fatal(err)
	}
	delivered := time.Date(2026, 10, 28, 21, 3, 0, 0, time.UTC)
	var lastDelivery atomic.Bool
	deps := Deps{Version: "dev", Token: testToken, Changes: change.NewService(db), Settings: settings.New(db), Secrets: sec,
		Hosts: host.Registry{change.HostGitHub: &host.GitHub{Token: "pat", App: app, APIBase: gh.URL}},
		GitHubIntake: GitHubIntake{PollInterval: 15 * time.Second, LastDelivery: func(repoID string) (time.Time, bool) {
			return delivered, lastDelivery.Load() && repoID != ""
		}}}
	a := &settingsAPI{testAPI: &testAPI{t: t, h: NewHandler(deps)}}
	rec := a.do(http.MethodPost, "/api/v1/repos", `{"host": "github", "url": "https://github.com/a/b", "protected_branches": ["main"], "pull_requests": true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", rec.Code, rec.Body.String())
	}
	repo := decode[change.Repo](t, rec)
	report := func() gitHubReportBody {
		t.Helper()
		rec := a.do(http.MethodGet, "/api/v1/repos/"+repo.ID+"/github", "")
		o.check("GitHubReport", rec)
		if rec.Code != http.StatusOK {
			t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
		}
		return decode[gitHubReportBody](t, rec)
	}

	// Everything in place: installed, the gate required by a ruleset, the webhook wired and polling too.
	_ = a.do(http.MethodPut, "/api/v1/settings/secrets/"+host.GitHubWebhookSecretKey, `{"value": "s3cret"}`)
	lastDelivery.Store(true)
	r := report()
	if !r.App.Configured || !r.App.Installed || r.App.Slug != "plumbix-gate" || !strings.Contains(r.App.Message, "plumbix-gate") || !strings.Contains(r.App.Message, "installed") {
		t.Errorf("app: %+v", r.App)
	}
	if len(r.Gate) != 1 || r.Gate[0].Branch != "main" || !r.Gate[0].Required || !r.Gate[0].Known || !strings.Contains(r.Gate[0].Message, "required on main") {
		t.Errorf("gate: %+v", r.Gate)
	}
	if r.Intake.Mode != "webhook" || !r.Intake.WebhookSecretSet || r.Intake.LastDeliveryAt == nil || !r.Intake.LastDeliveryAt.Equal(delivered) || r.Intake.PollInterval != "15s" || !strings.Contains(r.Intake.Message, "polling") {
		t.Errorf("intake: %+v", r.Intake)
	}

	// Not installed: the message links the installation page; the gate is still read with the token.
	gh.installed.Store(false)
	r = report()
	if r.App.Installed || !strings.Contains(r.App.InstallURL, "/apps/plumbix-gate/installations/new") || !strings.Contains(r.App.Message, "not installed") {
		t.Errorf("not installed: %+v", r.App)
	}
	if len(r.Gate) != 1 || !r.Gate[0].Required {
		t.Errorf("gate without the installation: %+v", r.Gate)
	}
	gh.installed.Store(true)

	// No rule names the gate and the classic protection says nothing is required: known, not required, and the message says what that means for merges.
	gh.gateInRule.Store(false)
	r = report()
	if len(r.Gate) != 1 || r.Gate[0].Required || !r.Gate[0].Known || !strings.Contains(r.Gate[0].Message, "not required on main") || !strings.Contains(r.Gate[0].Message, "not_required") {
		t.Errorf("not required: %+v", r.Gate)
	}
	// The classic protection cannot be read: unknown, with the reason.
	gh.protection.Store(http.StatusForbidden)
	r = report()
	if len(r.Gate) != 1 || r.Gate[0].Required || r.Gate[0].Known || !strings.Contains(r.Gate[0].Message, "unknown") || !strings.Contains(r.Gate[0].Message, "administration") {
		t.Errorf("unknown: %+v", r.Gate)
	}
	gh.gateInRule.Store(true)

	// Polling only, no delivery seen.
	_ = a.do(http.MethodDelete, "/api/v1/settings/secrets/"+host.GitHubWebhookSecretKey, "")
	lastDelivery.Store(false)
	r = report()
	if r.Intake.Mode != "polling" || r.Intake.WebhookSecretSet || r.Intake.LastDeliveryAt != nil || r.Intake.PollInterval != "15s" || !strings.Contains(r.Intake.Message, "every 15s") {
		t.Errorf("polling: %+v", r.Intake)
	}

	// No App: configured false, the message names the two secrets, nothing else is attempted for the App.
	deps.Hosts = host.Registry{change.HostGitHub: &host.GitHub{Token: "pat", APIBase: gh.URL}}
	a.h = NewHandler(deps)
	r = report()
	if r.App.Configured || r.App.Installed || !strings.Contains(r.App.Message, host.GitHubAppIDKey) || !strings.Contains(r.App.Message, host.GitHubAppKeyKey) {
		t.Errorf("no App: %+v", r.App)
	}
	if len(r.Gate) != 1 || !r.Gate[0].Required {
		t.Errorf("gate with the token alone: %+v", r.Gate)
	}

	// Another host: no report.
	rec = a.do(http.MethodPost, "/api/v1/repos", `{"host": "azuredevops", "url": "https://dev.azure.com/o/p/_git/r"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Azure repo: %d %s", rec.Code, rec.Body.String())
	}
	other := decode[change.Repo](t, rec)
	rec = a.do(http.MethodGet, "/api/v1/repos/"+other.ID+"/github", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(decode[apiError](t, rec).Error.Message, "azuredevops") {
		t.Errorf("Azure DevOps repository: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/r_nope/github", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repository: %d", rec.Code)
	}
}
