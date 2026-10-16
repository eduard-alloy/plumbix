// Package api implements the Plumbix HTTP API (version 1).
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/eduard-alloy/plumbix/internal/alert"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/engine"
	"github.com/eduard-alloy/plumbix/internal/run"
)

// Deps are the handler's dependencies.
type Deps struct {
	Version string
	Token   string          // bearer token every route except health requires
	Changes *change.Service // nil leaves the repo and change routes out
	Engine  *engine.Engine  // nil leaves the run routes out and skips auto-validation
	Runs    run.Store       // required with Engine
	Alerts  alert.Store     // nil leaves the alert routes out
	Logger  *slog.Logger    // nil discards logs
}

// NewHandler returns the HTTP handler for the public API.
func NewHandler(d Deps) http.Handler {
	version := d.Version
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/v1/pipelines/check", checkPipeline)
	if d.Changes != nil {
		ch := &changeHandlers{svc: d.Changes, engine: d.Engine, log: logger}
		registerChangeRoutes(protected, ch)
		if d.Engine != nil {
			registerRunRoutes(protected, &runHandlers{changeHandlers: ch, engine: d.Engine, runs: d.Runs})
		}
	}
	if d.Alerts != nil {
		registerAlertRoutes(protected, &alertHandlers{alerts: d.Alerts, log: logger})
	}
	protected.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})
	mux.Handle("/", requireToken(d.Token, protected))
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
