package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
	"github.com/eduard-alloy/plumbix/internal/store"
)

type settingsAPI struct {
	*testAPI
	secretsPath string
}

func newSettingsAPI(t *testing.T) *settingsAPI {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "plumbix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	path := filepath.Join(dir, ".env")
	sec, err := secrets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Deps{Version: "dev", Token: testToken, Changes: change.NewService(db), Settings: settings.New(db), Secrets: sec, Hosts: host.Live(sec.Values)})
	return &settingsAPI{testAPI: &testAPI{t: t, h: h}, secretsPath: path}
}

type settingsBody struct {
	AIModel  string `json:"ai_model"`
	Guidance string `json:"guidance"`
	Secrets  []struct {
		Key       string `json:"key"`
		Set       bool   `json:"set"`
		Known     bool   `json:"known"`
		Purpose   string `json:"purpose"`
		Host      string `json:"host"`
		UpdatedAt string `json:"updated_at"`
	} `json:"secrets"`
}

func (a *settingsAPI) secret(key string) (found bool, set bool, updated string) {
	a.t.Helper()
	s := decode[settingsBody](a.t, a.do(http.MethodGet, "/api/v1/settings", ""))
	for _, x := range s.Secrets {
		if x.Key == key {
			return true, x.Set, x.UpdatedAt
		}
	}
	return false, false, ""
}

func TestSettingsEndpoints(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t)

	rec := a.do(http.MethodGet, "/api/v1/settings", "")
	o.check("Settings", rec)
	s := decode[settingsBody](t, rec)
	if s.AIModel != "" || s.Guidance != "" || len(s.Secrets) < 4 {
		t.Fatalf("initial settings = %+v", s)
	}
	if s.Secrets[0].Key != "PLUMBIX_GITHUB_TOKEN" || s.Secrets[0].Set || !s.Secrets[0].Known || s.Secrets[0].Purpose == "" || s.Secrets[0].Host != "github" {
		t.Errorf("first secret = %+v", s.Secrets[0])
	}

	rec = a.do(http.MethodPut, "/api/v1/settings", `{"ai_model": "claude-sonnet-5-5", "guidance": " Prefer small diffs. "}`)
	o.check("Settings", rec)
	if s = decode[settingsBody](t, rec); rec.Code != http.StatusOK || s.AIModel != "claude-sonnet-5-5" || s.Guidance != "Prefer small diffs." {
		t.Errorf("after PUT: %d %+v", rec.Code, s)
	}
	rec = a.do(http.MethodPut, "/api/v1/settings", `{"guidance": ""}`)
	if s = decode[settingsBody](t, rec); s.Guidance != "" || s.AIModel != "claude-sonnet-5-5" {
		t.Errorf("empty clears only the given field: %+v", s)
	}
	rec = a.do(http.MethodPut, "/api/v1/settings", `{"ai_model": "not a model!"}`)
	o.check("Error", rec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad model: %d", rec.Code)
	}
	if rec := a.do(http.MethodPut, "/api/v1/settings", `{"unknown": 1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
}

func TestSecretEndpoints(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t)
	const value = "github_pat_very_secret_value"

	rec := a.do(http.MethodPut, "/api/v1/settings/secrets/PLUMBIX_GITHUB_TOKEN", `{"value": "`+value+`"}`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	if _, set, updated := a.secret("PLUMBIX_GITHUB_TOKEN"); !set || updated == "" {
		t.Errorf("after set: set %v updated %q", set, updated)
	}
	loaded, err := secrets.Load(a.secretsPath)
	if err != nil || loaded["PLUMBIX_GITHUB_TOKEN"] != value {
		t.Fatalf("file = %v, %v", loaded, err)
	}
	if info, _ := os.Stat(a.secretsPath); info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file mode = %v", info.Mode().Perm())
	}
	if body := a.do(http.MethodGet, "/api/v1/settings", "").Body.String(); strings.Contains(body, value) {
		t.Fatal("the settings response contains a secret value")
	}

	// Any key a pipeline step may name.
	if rec := a.do(http.MethodPut, "/api/v1/settings/secrets/MY_DEPLOY_KEY", `{"value": "x"}`); rec.Code != http.StatusNoContent {
		t.Errorf("custom key: %d %s", rec.Code, rec.Body.String())
	}
	if found, set, _ := a.secret("MY_DEPLOY_KEY"); !found || !set {
		t.Errorf("custom key listed = %v, set %v", found, set)
	}
	for path, body := range map[string]string{
		"/api/v1/settings/secrets/1bad":         `{"value": "x"}`,
		"/api/v1/settings/secrets/OK_KEY":       `{"value": ""}`,
		"/api/v1/settings/secrets/OK_KEY_TWO":   `{"value": "two\nlines"}`,
		"/api/v1/settings/secrets/OK_KEY_THREE": `{"nope": "x"}`,
	} {
		rec := a.do(http.MethodPut, path, body)
		o.check("Error", rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s %s: %d", path, body, rec.Code)
		}
	}

	if rec := a.do(http.MethodDelete, "/api/v1/settings/secrets/PLUMBIX_GITHUB_TOKEN", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, set, updated := a.secret("PLUMBIX_GITHUB_TOKEN"); set || updated != "" {
		t.Errorf("after delete: set %v updated %q", set, updated)
	}
	if rec := a.do(http.MethodDelete, "/api/v1/settings/secrets/NEVER_SET", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete unset: %d", rec.Code)
	}
	if loaded, _ := secrets.Load(a.secretsPath); loaded["PLUMBIX_GITHUB_TOKEN"] != "" || loaded["MY_DEPLOY_KEY"] != "x" {
		t.Errorf("file after delete = %v", loaded)
	}
}

func TestSecretCheck(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t)
	type check struct {
		OK        bool   `json:"ok"`
		ExpiresAt string `json:"expires_at"`
		Message   string `json:"message"`
	}
	rec := a.do(http.MethodPost, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN/check", "")
	o.check("SecretCheck", rec)
	if c := decode[check](t, rec); rec.Code != http.StatusOK || c.OK || !strings.Contains(c.Message, "not set") {
		t.Errorf("check without a token: %d %+v", rec.Code, c)
	}
	_ = a.do(http.MethodPut, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN", `{"value": "pat"}`)
	_ = a.do(http.MethodPut, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES", `{"value": "2027-01-01"}`)
	rec = a.do(http.MethodPost, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN/check", "")
	o.check("SecretCheck", rec)
	if c := decode[check](t, rec); !c.OK || !strings.HasPrefix(c.ExpiresAt, "2027-01-01") {
		t.Errorf("check with expiry: %+v", c)
	}
	_ = a.do(http.MethodPut, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES", `{"value": "soon"}`)
	if c := decode[check](t, a.do(http.MethodPost, "/api/v1/settings/secrets/PLUMBIX_AZURE_DEVOPS_TOKEN/check", "")); c.OK || c.Message == "" || strings.Contains(c.Message, "soon") || strings.Contains(c.Message, "pat") {
		t.Errorf("check with a bad expiry must not echo any secret value: %+v", c)
	}
	rec = a.do(http.MethodPost, "/api/v1/settings/secrets/ANTHROPIC_API_KEY/check", "")
	o.check("Error", rec)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("check of a non-host key: %d", rec.Code)
	}
}

func TestRepoSettings(t *testing.T) {
	o := loadOpenAPI(t)
	a := newSettingsAPI(t)
	repo := a.createRepo()
	rec := a.do(http.MethodGet, "/api/v1/repos/"+repo.ID+"/settings", "")
	o.check("RepoSettings", rec)
	if rec.Code != http.StatusOK || decode[struct{ Guidance string }](t, rec).Guidance != "" {
		t.Errorf("initial: %d %s", rec.Code, rec.Body.String())
	}
	rec = a.do(http.MethodPut, "/api/v1/repos/"+repo.ID+"/settings", `{"guidance": "Go code needs tests."}`)
	o.check("RepoSettings", rec)
	if decode[struct{ Guidance string }](t, rec).Guidance != "Go code needs tests." {
		t.Errorf("after PUT: %s", rec.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/repos/r_nope/settings", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repo: %d", rec.Code)
	}
	if rec := a.do(http.MethodPut, "/api/v1/repos/"+repo.ID+"/settings", `{"guidance": "`+strings.Repeat("x", 4001)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("too long: %d", rec.Code)
	}
}

func TestSettingsRoutesAbsentWithoutDeps(t *testing.T) {
	plain := newTestAPI(t)
	if rec := plain.do(http.MethodGet, "/api/v1/settings", ""); rec.Code != http.StatusNotFound {
		t.Errorf("without Settings the route must not exist: %d", rec.Code)
	}
}

func TestRedactSecret(t *testing.T) {
	if got := redactSecret("401 for token ghp_abc at github", "ghp_abc"); got != "401 for token *** at github" {
		t.Errorf("redact = %q", got)
	}
	if got := redactSecret("plain", ""); got != "plain" {
		t.Errorf("empty value changed the text: %q", got)
	}
	if got := redactSecrets("expiry soon? token pat1", map[string]string{"A": "soon", "B": "pat1"}); got != "expiry ***? token ***" {
		t.Errorf("redactSecrets = %q", got)
	}
}

// TestRepoToken: a repository's own token is set and removed through its
// settings, hidden from the global secrets list, and checked by fetching
// that repository's branches with it.
func TestRepoToken(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	a.repo.Commit("feat", map[string]string{"z.txt": "z\n"}, "z")
	url := a.repo.ServeHTTP("x-access-token", "repo-token-123")
	const id = "r_tokenrepo0001"
	if err := a.db.CreateRepo(ctxBG, change.Repo{ID: id, Host: "github", URL: url, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	type rs struct {
		Guidance       string `json:"guidance"`
		TokenSet       bool   `json:"token_set"`
		TokenUpdatedAt string `json:"token_updated_at"`
	}
	type check struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	rec := a.do(http.MethodGet, "/api/v1/repos/"+id+"/settings", "")
	o.check("RepoSettings", rec)
	if s := decode[rs](t, rec); s.TokenSet {
		t.Fatalf("initial = %+v", s)
	}
	rec = a.do(http.MethodPost, "/api/v1/repos/"+id+"/token/check", "")
	o.check("SecretCheck", rec)
	if c := decode[check](t, rec); c.OK {
		t.Fatalf("check without a token: %+v", c)
	}
	if rec := a.do(http.MethodPut, "/api/v1/repos/"+id+"/token", `{"value": "repo-token-123"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set repo token: %d %s", rec.Code, rec.Body.String())
	}
	if s := decode[rs](t, a.do(http.MethodGet, "/api/v1/repos/"+id+"/settings", "")); !s.TokenSet || s.TokenUpdatedAt == "" {
		t.Errorf("after set = %+v", s)
	}
	rec = a.do(http.MethodPost, "/api/v1/repos/"+id+"/token/check", "")
	o.check("SecretCheck", rec)
	if c := decode[check](t, rec); !c.OK || !strings.Contains(c.Message, "2 branches") || strings.Contains(c.Message, "repo-token-123") {
		t.Errorf("check with the repo token: %+v", c)
	}
	if body := a.do(http.MethodGet, "/api/v1/settings", "").Body.String(); strings.Contains(body, "PLUMBIX_REPO_TOKEN") || strings.Contains(body, "repo-token-123") {
		t.Errorf("repository tokens leak into the global list: %s", body)
	}
	if rec := a.do(http.MethodDelete, "/api/v1/repos/"+id+"/token", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", rec.Code)
	}
	if s := decode[rs](t, a.do(http.MethodGet, "/api/v1/repos/"+id+"/settings", "")); s.TokenSet {
		t.Errorf("after remove = %+v", s)
	}
	if rec := a.do(http.MethodPut, "/api/v1/repos/r_nope/token", `{"value": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repo: %d", rec.Code)
	}
}
