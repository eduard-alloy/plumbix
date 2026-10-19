package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const createToken = "ghp_create_SECRET"

// TestGitHubCreateRepository (WU-042): under the token's own account when
// no owner or the account's login is given, under an organisation
// otherwise; private by choice; the token only in the Authorization header.
func TestGitHubCreateRepository(t *testing.T) {
	var calls []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+createToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.String(), createToken) {
			t.Error("token in the URL")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "eduard"})
		case "/user/repos", "/orgs/acme/repos":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			if b["name"] == "taken" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, `{"message":"Repository creation failed.","errors":[{"message":"name already exists on this account"}]}`)
				return
			}
			owner := "eduard"
			if strings.HasPrefix(r.URL.Path, "/orgs/") {
				owner = "acme"
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": owner + "/" + b["name"].(string)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	g := &GitHub{Token: createToken, APIBase: srv.URL}
	ctx := context.Background()
	url, err := g.CreateRepository(ctx, "", "", "notes", true)
	if err != nil || url != "https://github.com/eduard/notes" {
		t.Fatalf("own account: %q %v", url, err)
	}
	if url, err := g.CreateRepository(ctx, "", "eduard", "notes2", false); err != nil || url != "https://github.com/eduard/notes2" {
		t.Errorf("own login as owner: %q %v", url, err)
	}
	if url, err := g.CreateRepository(ctx, "", "acme", "billing", true); err != nil || url != "https://github.com/acme/billing" {
		t.Errorf("organisation: %q %v", url, err)
	}
	if bodies[0]["private"] != true || bodies[1]["private"] != false || bodies[0]["auto_init"] != false {
		t.Errorf("bodies = %v", bodies)
	}
	if _, err := g.CreateRepository(ctx, "", "", "taken", true); err == nil || !strings.Contains(err.Error(), "name already exists") {
		t.Errorf("taken name: %v", err)
	}
	// A token given for this repository wins over the host token; a rejected one says so without echoing it.
	other := &GitHub{Token: "host-token", APIBase: srv.URL}
	if _, err := other.CreateRepository(ctx, createToken, "", "notes3", true); err != nil {
		t.Errorf("given token: %v", err)
	}
	if _, err := other.CreateRepository(ctx, "", "", "notes4", true); err == nil || !strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), "host-token") {
		t.Errorf("rejected token: %v", err)
	}
	if _, err := (&GitHub{APIBase: srv.URL}).CreateRepository(ctx, "", "", "x", true); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
}

// TestAzureDevOpsCreateRepository: in an organisation's project, with the
// token as basic auth; a sign-in page (203) means the token was rejected.
func TestAzureDevOpsCreateRepository(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+createToken))
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusNonAuthoritativeInfo)
			_, _ = io.WriteString(w, "<html>sign in</html>")
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/acme/ledger/_apis/git/repositories" || r.URL.Query().Get("api-version") == "" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"remoteUrl": "https://acme@dev.azure.com/acme/ledger/_git/" + b["name"]})
	}))
	defer srv.Close()
	a := &AzureDevOps{Token: createToken, APIBase: srv.URL}
	url, err := a.CreateRepository(context.Background(), "", "acme", "ledger", "payments")
	if err != nil || url != "https://dev.azure.com/acme/ledger/_git/payments" {
		t.Fatalf("create: %q %v", url, err)
	}
	if _, err := (&AzureDevOps{Token: "bad", APIBase: srv.URL}).CreateRepository(context.Background(), "", "acme", "ledger", "x"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("rejected token: %v", err)
	}
	if _, err := a.CreateRepository(context.Background(), "", "", "ledger", "x"); err == nil {
		t.Error("missing organisation accepted")
	}
}
