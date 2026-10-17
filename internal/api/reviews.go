package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// MaxReviewWait bounds the long poll of GET /reviews/next, under the usual
// proxy and client timeouts.
const MaxReviewWait = 55 * time.Second

// reviewPollEvery is how often a long poll looks again.
var reviewPollEvery = time.Second

// clientGone reports whether the poller hung up; a test overrides it.
var clientGone = func(r *http.Request) bool { return r.Context().Err() != nil }

// reviewHandlers serve external reviewers (WU-023): the next brief, and the
// verdict they post back.
type reviewHandlers struct {
	*changeHandlers
}

func registerReviewRoutes(mux *http.ServeMux, h *reviewHandlers) {
	mux.HandleFunc("GET /api/v1/reviews/next", h.next)
	mux.HandleFunc("POST /api/v1/reviews/{id}/verdict", h.verdict)
	mux.HandleFunc("GET /api/v1/reviewers", h.reviewers)
}

// reviewers lists the external reviewers the server has heard from since
// it started, most recent first (WU-026).
func (h *reviewHandlers) reviewers(w http.ResponseWriter, _ *http.Request) {
	list := h.engine.Reviewers()
	if list == nil {
		list = []engine.Reviewer{}
	}
	writeJSON(w, http.StatusOK, struct {
		Reviewers []engine.Reviewer `json:"reviewers"`
	}{list})
}

// next claims the oldest waiting review for the reviewer, long-polling up
// to wait seconds, and answers 204 when nothing is waiting.
func (h *reviewHandlers) next(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wait := time.Duration(0)
	if s := q.Get("wait"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeProblems(w, "the request has 1 problem(s)", []change.Problem{{Field: "wait", Message: "must be a number of seconds, 0 or more"}})
			return
		}
		wait = min(time.Duration(n)*time.Second, MaxReviewWait)
	}
	deadline := time.Now().Add(wait)
	for {
		b, ok, err := h.engine.ClaimReview(r.Context(), q.Get("repo_id"), q.Get("reviewer"))
		if err != nil {
			h.fail(w, err)
			return
		}
		if ok {
			// The claim happened in the store; if the poller hung up meanwhile
			// the token would be lost with it, so the claim is given back.
			if clientGone(r) || writeJSONErr(w, http.StatusOK, b) != nil {
				// The poller hung up, or the brief did not reach it: the token
				// would be lost with it, so the claim is given back.
				bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := h.engine.ReleaseReview(bg, b.ID); err != nil {
					h.log.Warn("release review after the client left", "review", b.ID, "err", err)
				}
			}
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(min(remaining, reviewPollEvery)):
		}
	}
}

// verdictBody is what a reviewer posts back: the token from its brief and
// the verdict in the validator contract's shape.
type verdictBody struct {
	Token   string          `json:"token"`
	Verdict json.RawMessage `json:"verdict"`
}

// reviewAccepted is the answer to a posted verdict.
type reviewAccepted struct {
	Status   string `json:"status"`
	ReviewID string `json:"review_id"`
}

func (h *reviewHandlers) verdict(w http.ResponseWriter, r *http.Request) {
	var body verdictBody
	if !decodeJSON(w, r, &body) {
		return
	}
	var problems []change.Problem
	if body.Token == "" {
		problems = append(problems, change.Problem{Field: "token", Message: "is required: the token that came with the brief"})
	}
	if len(body.Verdict) == 0 {
		problems = append(problems, change.Problem{Field: "verdict", Message: "is required: a verdict in the validator contract's shape"})
	}
	if len(problems) > 0 {
		writeProblems(w, "the request has "+strconv.Itoa(len(problems))+" problem(s)", problems)
		return
	}
	id := r.PathValue("id")
	if err := h.engine.PostReviewVerdict(r.Context(), id, body.Token, body.Verdict); err != nil {
		var inv *validation.InvalidVerdictError
		if errors.As(err, &inv) {
			for _, p := range inv.Problems {
				problems = append(problems, change.Problem{Field: "verdict", Message: p})
			}
			writeProblems(w, "the verdict has "+strconv.Itoa(len(problems))+" problem(s)", problems)
			return
		}
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reviewAccepted{Status: "accepted", ReviewID: id})
}
