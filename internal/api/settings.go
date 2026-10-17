package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
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
	heads    func(ctx context.Context, repo change.Repo) (map[string]string, error) // fetches a repo's branch heads; nil disables the repo token check
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
		mux.HandleFunc("PUT /api/v1/repos/{id}/token", h.setRepoToken)
		mux.HandleFunc("DELETE /api/v1/repos/{id}/token", h.removeRepoToken)
		mux.HandleFunc("POST /api/v1/repos/{id}/token/check", h.checkRepoToken)
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
	AITools  string         `json:"ai_tools"`
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
	tools, err := h.settings.AITools(ctx)
	if err != nil {
		return settingsView{}, err
	}
	v := settingsView{AIModel: model, AITools: tools, Guidance: g.Global, Secrets: []secretStatus{}}
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
		if host.IsRepoTokenKey(k) {
			continue // shown on the repository, not here
		}
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
		AITools  *string `json:"ai_tools"`
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
	if body.AITools != nil {
		if err := h.settings.SetAITools(r.Context(), *body.AITools); err != nil {
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
		writeError(w, http.StatusBadRequest, "invalid_request", redactSecret(err.Error(), body.Value))
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
		writeJSON(w, http.StatusOK, secretCheck{Message: "check failed: " + redactSecrets(err.Error(), h.secrets.Values())})
	case known:
		writeJSON(w, http.StatusOK, secretCheck{OK: true, ExpiresAt: &expires, Message: "token accepted; expires " + expires.UTC().Format("2006-01-02")})
	default:
		writeJSON(w, http.StatusOK, secretCheck{OK: true, Message: "token accepted; the host reports no expiry"})
	}
}

type repoSettings struct {
	Guidance       string     `json:"guidance"`
	TokenSet       bool       `json:"token_set"`
	TokenUpdatedAt *time.Time `json:"token_updated_at,omitempty"`
}

func (h *settingsHandlers) repoView(ctx context.Context, repo change.Repo) (repoSettings, error) {
	g, err := h.settings.Guidance(ctx, repo.ID)
	if err != nil {
		return repoSettings{}, err
	}
	v := repoSettings{Guidance: g.Repository}
	key := host.RepoTokenKey(repo.ID)
	if _, set := h.secrets.Get(key); set {
		v.TokenSet = true
		if when, ok, err := h.settings.SecretUpdated(ctx, key); err != nil {
			return repoSettings{}, err
		} else if ok {
			v.TokenUpdatedAt = &when
		}
	}
	return v, nil
}

func (h *settingsHandlers) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	v, err := h.repoView(r.Context(), repo)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setRepoToken gives one repository its own token, which wins over the
// host-wide one for that repository.
func (h *settingsHandlers) setRepoToken(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	key := host.RepoTokenKey(repo.ID)
	if err := h.secrets.Set(key, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", redactSecret(err.Error(), body.Value))
		return
	}
	if err := h.settings.MarkSecret(r.Context(), key); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("repository token set", "repo", repo.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *settingsHandlers) removeRepoToken(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	key := host.RepoTokenKey(repo.ID)
	if err := h.secrets.Unset(key); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.settings.ForgetSecret(r.Context(), key); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("repository token removed", "repo", repo.ID)
	w.WriteHeader(http.StatusNoContent)
}

// checkRepoToken fetches the repository's branch heads with whatever
// credentials apply to it now: its own token, else the host-wide one.
func (h *settingsHandlers) checkRepoToken(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	if h.heads == nil {
		writeJSON(w, http.StatusOK, secretCheck{Message: "no engine to fetch with"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), tokenCheckTimeout)
	defer cancel()
	heads, err := h.heads(ctx, repo)
	if err != nil {
		writeJSON(w, http.StatusOK, secretCheck{Message: "fetch failed: " + redactSecrets(err.Error(), h.secrets.Values())})
		return
	}
	_, own := h.secrets.Get(host.RepoTokenKey(repo.ID))
	with := "the host token"
	if own {
		with = "the repository's own token"
	}
	writeJSON(w, http.StatusOK, secretCheck{OK: true, Message: fmt.Sprintf("fetched %d branches with %s", len(heads), with)})
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

// redactSecret hides value wherever it appears in text.
func redactSecret(text, value string) string {
	if value == "" {
		return text
	}
	return strings.ReplaceAll(text, value, "***")
}

// redactSecrets hides every secret value in text, so an adapter's error
// can never echo one.
func redactSecrets(text string, values map[string]string) string {
	for _, v := range values {
		text = redactSecret(text, v)
	}
	return text
}

func (h *settingsHandlers) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, change.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	h.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
