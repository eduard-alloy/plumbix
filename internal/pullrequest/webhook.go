package pullrequest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
)

// maxWebhookBytes bounds a delivery's body.
const maxWebhookBytes = 4 << 20

// Webhook receives GitHub's deliveries for pull requests, reviews and
// re-requested checks and feeds the Syncer. Every delivery must carry an
// HMAC-SHA256 signature made with the webhook secret; without a secret the
// endpoint refuses everything.
type Webhook struct {
	Secret func() string
	Syncer *Syncer
	Repos  interface {
		ListRepos(ctx context.Context) ([]change.Repo, error)
	}
	Log *slog.Logger
	// now is the clock for the delivery record; nil means time.Now.
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time // repository id → when a signed delivery for it last arrived
}

// LastDelivery says when a signed delivery for the repository last reached
// the handler since the server started, handled or not (WU-061). A ping
// names no repository and is not counted.
func (w *Webhook) LastDelivery(repoID string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.last[repoID]
	return at, ok
}

func (w *Webhook) remember(repoID string) {
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last == nil {
		w.last = map[string]time.Time{}
	}
	w.last[repoID] = now().UTC()
}

type delivery struct {
	Action     string `json:"action"`
	Repository struct {
		URL string `json:"html_url"`
	} `json:"repository"`
	PullRequest *host.PullRequest `json:"pull_request"`
	Review      *host.Review      `json:"review"`
	CheckSuite  *struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun *struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

func (w *Webhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	secret := ""
	if w.Secret != nil {
		secret = w.Secret()
	}
	if secret == "" {
		http.Error(rw, "webhook secret not set: set "+host.GitHubWebhookSecretKey, http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(rw, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !signed(secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(rw, "signature missing or wrong", http.StatusUnauthorized)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(rw).Encode(map[string]any{"handled": false})
		return
	}
	var d delivery
	if err := json.Unmarshal(body, &d); err != nil {
		http.Error(rw, "not a JSON delivery", http.StatusBadRequest)
		return
	}
	repo, ok, err := w.repoFor(r.Context(), d.Repository.URL)
	if err != nil {
		w.Log.Error("webhook: list repositories", "err", err)
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(rw, "no registered repository watches pull requests at "+d.Repository.URL, http.StatusNotFound)
		return
	}
	w.remember(repo.ID)
	handled, err := w.dispatch(r.Context(), repo, event, d)
	if err != nil {
		w.Log.Error("webhook", "event", event, "action", d.Action, "repo", repo.ID, "err", err)
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	status := http.StatusAccepted
	if !handled {
		status = http.StatusOK
	}
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(map[string]any{"handled": handled})
}

// dispatch acts on one delivery; handled is false for events and actions
// the gate does not use.
func (w *Webhook) dispatch(ctx context.Context, repo change.Repo, event string, d delivery) (bool, error) {
	switch event {
	case "pull_request":
		if d.PullRequest == nil {
			return false, nil
		}
		switch d.Action {
		case "opened", "reopened", "synchronize", "closed", "ready_for_review":
			return true, w.Syncer.Apply(ctx, repo, *d.PullRequest)
		}
		return false, nil
	case "pull_request_review":
		if d.Action != "submitted" || d.Review == nil || d.PullRequest == nil {
			return false, nil
		}
		c, err := w.Syncer.Store.ChangeByPullRequest(ctx, repo.ID, d.PullRequest.Number)
		if errors.Is(err, change.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, w.Syncer.ApplyReviews(ctx, c, []host.Review{*d.Review})
	case "check_suite", "check_run":
		if d.Action != "rerequested" {
			return false, nil
		}
		var numbers []struct {
			Number int `json:"number"`
		}
		switch {
		case d.CheckSuite != nil:
			numbers = d.CheckSuite.PullRequests
		case d.CheckRun != nil:
			numbers = d.CheckRun.PullRequests
		}
		handled := false
		for _, n := range numbers {
			err := w.Syncer.Revalidate(ctx, repo, n.Number)
			if errors.Is(err, change.ErrNotFound) || errors.Is(err, change.ErrInvalidState) {
				continue
			}
			if err != nil {
				return handled, err
			}
			handled = true
		}
		return handled, nil
	}
	return false, nil
}

// repoFor finds the registered GitHub repository at url that watches pull
// requests.
func (w *Webhook) repoFor(ctx context.Context, url string) (change.Repo, bool, error) {
	repos, err := w.Repos.ListRepos(ctx)
	if err != nil {
		return change.Repo{}, false, err
	}
	want := normalizeURL(url)
	for _, r := range repos {
		if r.Host == change.HostGitHub && r.PullRequests && normalizeURL(r.URL) == want {
			return r, true, nil
		}
	}
	return change.Repo{}, false, nil
}

func normalizeURL(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(u), "/"), ".git")
	return strings.ToLower(u)
}

// signed checks GitHub's sha256=<hex> signature of the body.
func signed(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}
