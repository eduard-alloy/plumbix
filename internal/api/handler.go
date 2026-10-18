// Package api implements the Plumbix HTTP API (version 1).
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/auth"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/images"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

// Deps are the handler's dependencies.
type Deps struct {
	Version string
	Token   string          // bearer token every route except health requires
	Changes *change.Service // nil leaves the repo and change routes out
	Engine  *engine.Engine  // nil leaves the run routes out and skips auto-validation
	Runs    run.Store       // required with Engine
	Alerts  alert.Store     // nil leaves the alert routes out
	Events  EventSource     // nil leaves the event stream out
	Logger  *slog.Logger    // nil discards logs
	// Sessions enables browser sign-in; nil leaves it out. Origin is the
	// address browsers must use, such as http://127.0.0.1:6400: login
	// links point there, and cookie requests must carry that Host.
	Sessions *auth.Sessions
	Origin   string
	Web      http.Handler // the browser app, served outside /api/; nil answers 404 there
	// Settings and Secrets together enable the settings routes; Hosts serves
	// the on-demand token check.
	Settings *settings.Service
	Secrets  *secrets.Manager
	Hosts    host.Source
	// Images enables the Images page's routes (WU-032); needs Engine and Changes.
	Images *images.Service
}

// NewHandler returns the HTTP handler for the public API.
func NewHandler(d Deps) http.Handler {
	version := d.Version
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	az := newAuthz(d.Token, d.Sessions, d.Origin)
	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/v1/pipelines/check", checkPipeline)
	if d.Changes != nil {
		ch := &changeHandlers{svc: d.Changes, engine: d.Engine, log: logger}
		registerChangeRoutes(protected, ch)
		if d.Engine != nil {
			registerRunRoutes(protected, &runHandlers{changeHandlers: ch, engine: d.Engine, runs: d.Runs})
			registerPipelineRoutes(protected, &pipelineHandlers{svc: d.Changes, engine: d.Engine, runs: d.Runs, log: logger})
			registerReviewRoutes(protected, &reviewHandlers{changeHandlers: ch})
			if d.Images != nil {
				registerImageRoutes(protected, &imageHandlers{images: d.Images, svc: d.Changes, engine: d.Engine, log: logger})
			}
		}
	}
	if d.Alerts != nil {
		registerAlertRoutes(protected, &alertHandlers{alerts: d.Alerts, log: logger})
	}
	if d.Events != nil {
		registerEventRoutes(protected, &eventHandlers{events: d.Events, log: logger})
	}
	if d.Settings != nil && d.Secrets != nil {
		sh := &settingsHandlers{settings: d.Settings, secrets: d.Secrets, hosts: d.Hosts, changes: d.Changes, log: logger}
		if d.Engine != nil {
			sh.heads = d.Engine.ListHeads
		}
		registerSettingsRoutes(protected, sh)
	}
	protected.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})
	if d.Sessions != nil {
		registerSessionRoutes(mux, protected, az, &sessionHandlers{sessions: d.Sessions, origin: d.Origin, authz: az, log: logger})
	}
	mux.Handle("/api/", az.wrap(protected))
	if d.Web != nil {
		mux.Handle("/", d.Web)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "no such page; the web interface is not built into this server")
		})
	}
	return mux
}

// errorBody is the error shape shared by every endpoint.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeProblems(w http.ResponseWriter, message string, problems []change.Problem) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
		Code: "invalid_request", Message: message, Details: map[string]any{"problems": problems},
	}})
}

// writeJSONErr is writeJSON for callers that must know whether the body
// reached the client.
func writeJSONErr(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
