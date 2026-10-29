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
	"time"

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
	if c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1); c.State != change.Approved || c.HumanApproval.Reason != "fine" || c.HumanApproval.By != "lead" || c.HumanApproval.Source != change.ApprovalSourceGitHubReview {
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

// TestWebhookRemembersLastDelivery: the handler remembers when a signed
// delivery last reached it for each repository, handled or not, so the
// repository's GitHub report can say the webhook is wired (WU-061). A ping
// names no repository and is not remembered.
func TestWebhookRemembersLastDelivery(t *testing.T) {
	h := newHarness(t)
	secret := "s3cret"
	now := time.Date(2026, 10, 28, 21, 0, 0, 0, time.UTC)
	w := &Webhook{Secret: func() string { return secret }, Syncer: h.s, Repos: h.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return now }}
	if _, ok := w.LastDelivery(h.repo.ID); ok {
		t.Fatal("a delivery remembered before any arrived")
	}
	if rec := deliver(t, w, secret, "ping", map[string]any{"zen": "x"}); rec.Code != http.StatusOK {
		t.Fatalf("ping: %d", rec.Code)
	}
	if _, ok := w.LastDelivery(h.repo.ID); ok {
		t.Error("a ping was remembered as a delivery for the repository")
	}
	type repoBody struct {
		URL string `json:"html_url"`
	}
	labeled := map[string]any{"action": "labeled", "repository": repoBody{"https://github.com/a/b.git"}, "pull_request": pr(1, "T", "")}
	if rec := deliver(t, w, secret, "pull_request", labeled); rec.Code != http.StatusOK {
		t.Fatalf("labeled: %d %s", rec.Code, rec.Body.String())
	}
	if at, ok := w.LastDelivery(h.repo.ID); !ok || !at.Equal(now) {
		t.Errorf("last delivery = %v, %v; want %v", at, ok, now)
	}
	if rec := deliver(t, w, "wrong", "pull_request", labeled); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", rec.Code)
	}
	if _, ok := w.LastDelivery("r_other"); ok {
		t.Error("a delivery remembered for another repository")
	}
}

// TestWebhookEditedUpdatesTheChange: the edited action re-reads the title,
// body and criteria into the change (WU-062).
func TestWebhookEditedUpdatesTheChange(t *testing.T) {
	h := newHarness(t)
	h.eng.steps = []string{"build", "test"}
	secret := "s3cret"
	w := &Webhook{Secret: func() string { return secret }, Syncer: h.s, Repos: h.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	type repoBody struct {
		URL string `json:"html_url"`
	}
	opened := map[string]any{"action": "opened", "repository": repoBody{"https://github.com/a/b.git"}, "pull_request": pr(1, "T", "## Acceptance criteria\n- works\n")}
	if rec := deliver(t, w, secret, "pull_request", opened); rec.Code != http.StatusAccepted {
		t.Fatalf("opened: %d %s", rec.Code, rec.Body.String())
	}
	edited := map[string]any{"action": "edited", "repository": repoBody{"https://github.com/a/b.git"}, "pull_request": pr(1, "T2", "## Acceptance criteria\n- works (check: test)\n")}
	if rec := deliver(t, w, secret, "pull_request", edited); rec.Code != http.StatusAccepted {
		t.Fatalf("edited: %d %s", rec.Code, rec.Body.String())
	}
	c, _ := h.db.ChangeByPullRequest(ctx, h.repo.ID, 1)
	if c.Title != "T2" || len(c.AcceptanceCriteria) != 1 || c.AcceptanceCriteria[0].Check == nil || c.AcceptanceCriteria[0].Check.Steps[0] != "test" {
		t.Errorf("after edited: %q %+v", c.Title, c.AcceptanceCriteria)
	}
}
