package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/secrets"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

// tokenCheckTimeout bounds an on-demand host token check.
const tokenCheckTimeout = 20 * time.Second

type settingsHandlers struct {
	settings *settings.Service
	secrets  *secrets.Manager
	hosts    host.Source
	changes  *change.Service
	log      *slog.Logger
}

func registerSettingsRoutes(mux *http.ServeMux, h *settingsHandlers) {
	mux.HandleFunc("GET /api/v1/settings", h.get)
	mux.HandleFunc("PUT /api/v1/settings", h.update)
	mux.HandleFunc("PUT /api/v1/settings/secrets/{key}", h.setSecret)
	mux.HandleFunc("DELETE /api/v1/settings/secrets/{key}", h.removeSecret)
	mux.HandleFunc("POST /api/v1/settings/secrets/{key}/check", h.checkSecret)
	if h.changes != nil {
		mux.HandleFunc("GET /api/v1/repos/{id}/settings", h.getRepo)
		mux.HandleFunc("PUT /api/v1/repos/{id}/settings", h.updateRepo)
	}
}

type secretStatus struct {
	Key       string     `json:"key"`
	Set       bool       `json:"set"`
	Known     bool       `json:"known"`
	Purpose   string     `json:"purpose"`
	Host      string     `json:"host,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type settingsView struct {
	AIModel  string         `json:"ai_model"`
	Guidance string         `json:"guidance"`
	Secrets  []secretStatus `json:"secrets"`
}

func (h *settingsHandlers) view(ctx context.Context) (settingsView, error) {
	g, err := h.settings.Guidance(ctx, "")
	if err != nil {
		return settingsView{}, err
	}
	model, err := h.settings.AIModel(ctx)
	if err != nil {
		return settingsView{}, err
	}
	v := settingsView{AIModel: model, Guidance: g.Global, Secrets: []secretStatus{}}
	listed := map[string]bool{}
	add := func(key, purpose, hostKind string, known bool) error {
		_, set := h.secrets.Get(key)
		s := secretStatus{Key: key, Set: set, Known: known, Purpose: purpose, Host: hostKind}
		if set {
			when, ok, err := h.settings.SecretUpdated(ctx, key)
			if err != nil {
				return err
			}
			if ok {
				s.UpdatedAt = &when
			}
		}
		v.Secrets = append(v.Secrets, s)
		listed[key] = true
		return nil
	}
	for _, k := range settings.KnownSecrets() {
		if err := add(k.Key, k.Purpose, k.Host, true); err != nil {
			return settingsView{}, err
		}
	}
	extra := h.secrets.Keys()
	sort.Strings(extra)
	for _, k := range extra {
		if !listed[k] {
			if err := add(k, "Named by a pipeline step's secrets list", "", false); err != nil {
				return settingsView{}, err
			}
		}
	}
	return v, nil
}

func (h *settingsHandlers) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.view(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *settingsHandlers) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AIModel  *string `json:"ai_model"`
		Guidance *string `json:"guidance"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.AIModel != nil {
		if err := h.settings.SetAIModel(r.Context(), *body.AIModel); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if body.Guidance != nil {
		if err := h.settings.SetGlobalGuidance(r.Context(), *body.Guidance); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	h.get(w, r)
}

// setSecret writes a value into the secrets file. The value is read from
// the body and never logged.
func (h *settingsHandlers) setSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.secrets.Set(key, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.settings.MarkSecret(r.Context(), key); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("secret set", "key", key)
	w.WriteHeader(http.StatusNoContent)
}

func (h *settingsHandlers) removeSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := h.secrets.Unset(key); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.settings.ForgetSecret(r.Context(), key); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("secret removed", "key", key)
	w.WriteHeader(http.StatusNoContent)
}

type secretCheck struct {
	OK        bool       `json:"ok"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Message   string     `json:"message"`
}

// checkSecret runs a host's token check with the current value.
func (h *settingsHandlers) checkSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var kind string
	for _, k := range settings.KnownSecrets() {
		if k.Key == key {
			kind = k.Host
		}
	}
	if kind == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", key+" is not a git host token")
		return
	}
	if _, set := h.secrets.Get(key); !set {
		writeJSON(w, http.StatusOK, secretCheck{Message: key + " is not set"})
		return
	}
	adapter, ok := h.hosts.Hosts()[kind]
	if !ok {
		writeJSON(w, http.StatusOK, secretCheck{Message: "no adapter for " + kind})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), tokenCheckTimeout)
	defer cancel()
	expires, known, err := adapter.TokenExpiry(ctx)
	switch {
	case err != nil:
		writeJSON(w, http.StatusOK, secretCheck{Message: "check failed: " + err.Error()})
	case known:
		writeJSON(w, http.StatusOK, secretCheck{OK: true, ExpiresAt: &expires, Message: "token accepted; expires " + expires.UTC().Format("2006-01-02")})
	default:
		writeJSON(w, http.StatusOK, secretCheck{OK: true, Message: "token accepted; the host reports no expiry"})
	}
}

func (h *settingsHandlers) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	g, err := h.settings.Guidance(r.Context(), repo.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"guidance": g.Repository})
}

func (h *settingsHandlers) updateRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var body struct {
		Guidance string `json:"guidance"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.settings.SetRepoGuidance(r.Context(), repo.ID, body.Guidance); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.getRepo(w, r)
}

func (h *settingsHandlers) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, change.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	h.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
