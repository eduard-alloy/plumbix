package pullrequest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
)

func deliver(t *testing.T, w *Webhook, secret, event string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, req)
	return rec
}

// TestWebhookFeedsIntake: a signed pull_request delivery opens the change,
// a review approves it, a re-requested check suite validates again; bad or
// missing signatures and unknown repositories are refused.
func TestWebhookFeedsIntake(t *testing.T) {
	h := newHarness(t)
	secret := "s3cret"
	w := &Webhook{Secret: func() string { return secret }, Syncer: h.s, Repos: h.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	type repoBody struct {
		URL string `json:"html_url"`
	}
	opened := map[string]any{"action": "opened", "repository": repoBody{"https://github.com/a/b.git"}, "pull_request": pr(1, "T", "## Acceptance criteria\n- works\n")}

	if rec := deliver(t, w, "", "pull_request", opened); rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: %d", rec.Code)
	}
	if rec := deliver(t, w, "wrong", "pull_request", opened); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", rec.Code)
	}
	if rec := deliver(t, w, secret, "ping", map[string]any{"zen": "x"}); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"handled":false}` {
		t.Errorf("ping: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliver(t, w, secret, "pull_request", opened); rec.Code != http.StatusAccepted {
		t.Fatalf("opened: %d %s", rec.Code, rec.Body.String())
	}
	c, err := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if err != nil || c.Title != "T" || len(c.AcceptanceCriteria) != 1 || c.State != change.NeedsHuman {
		t.Fatalf("change = %+v, %v", c, err)
	}

	review := map[string]any{"action": "submitted", "repository": repoBody{"https://github.com/a/b"}, "pull_request": pr(1, "T", ""),
		"review": host.Review{ID: 9, Author: host.Actor{Login: "lead", Type: "User"}, AuthorAssociation: "OWNER", State: "approved", CommitSHA: "h1", Body: "fine"}}
	if rec := deliver(t, w, secret, "pull_request_review", review); rec.Code != http.StatusAccepted {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	if c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1); c.State != change.Approved || c.HumanApproval.Reason != "Approved on GitHub by lead: fine" {
		t.Errorf("after review: %s %+v", c.State, c.HumanApproval)
	}

	rerun := map[string]any{"action": "rerequested", "repository": repoBody{"https://github.com/a/b"}, "check_suite": map[string]any{"pull_requests": []map[string]int{{"number": 1}}}}
	if rec := deliver(t, w, secret, "check_suite", rerun); rec.Code != http.StatusAccepted || len(h.eng.validated) != 2 {
		t.Errorf("rerequested: %d, validations %v", rec.Code, h.eng.validated)
	}
	if rec := deliver(t, w, secret, "issues", map[string]any{"action": "opened", "repository": repoBody{"https://github.com/a/b"}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "false") {
		t.Errorf("unused event: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliver(t, w, secret, "pull_request", map[string]any{"action": "opened", "repository": repoBody{"https://github.com/a/other"}, "pull_request": pr(5, "x", "")}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown repository: %d", rec.Code)
	}
	secret = ""
	if rec := deliver(t, w, "s3cret", "pull_request", opened); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no secret: %d", rec.Code)
	}
}
