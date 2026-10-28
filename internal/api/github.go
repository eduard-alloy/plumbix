package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/host"
)

// The repository GitHub report (WU-061): the per-repository facts behind
// the pull-request gate, read live and as the App, never stored. The App's
// id and key stay one per host; what varies per repository is whether the
// App is installed there, whether the gate is required on each protected
// branch, and how pull requests reach the server.

// gateCheck is the check run GitHub must require for the gate to hold.
const gateCheck = "plumbix/gate"

type gitHubReport struct {
	App    gitHubAppStatus    `json:"app"`
	Gate   []gitHubGateStatus `json:"gate"`
	Intake gitHubIntakeStatus `json:"intake"`
}

type gitHubAppStatus struct {
	Configured bool   `json:"configured"`
	Slug       string `json:"slug,omitempty"`
	Installed  bool   `json:"installed"`
	InstallURL string `json:"install_url,omitempty"`
	Message    string `json:"message"`
}

type gitHubGateStatus struct {
	Branch   string `json:"branch"`
	Required bool   `json:"required"`
	Known    bool   `json:"known"`
	Message  string `json:"message"`
}

type gitHubIntakeStatus struct {
	Mode             string     `json:"mode"` // webhook, polling, none
	WebhookSecretSet bool       `json:"webhook_secret_set"`
	LastDeliveryAt   *time.Time `json:"last_delivery_at,omitempty"`
	PollInterval     string     `json:"poll_interval,omitempty"`
	Message          string     `json:"message"`
}

// gitHubReporter is what the report asks of the GitHub adapter;
// *host.GitHub implements it.
type gitHubReporter interface {
	AppCheck(ctx context.Context) (string, error)
	AppInstalled(ctx context.Context, repo change.Repo) (bool, error)
	RequiredChecks(ctx context.Context, repo change.Repo, branch string) (contexts []string, known bool, err error)
}

func (h *settingsHandlers) gitHubReport(w http.ResponseWriter, r *http.Request) {
	repo, err := h.changes.GetRepo(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	if repo.Host != change.HostGitHub {
		writeError(w, http.StatusNotFound, "not_found", "no GitHub integration for a "+repo.Host+" repository")
		return
	}
	var adapter gitHubReporter
	if h.hosts != nil {
		adapter, _ = h.hosts.Hosts()[change.HostGitHub].(gitHubReporter)
	}
	ctx, cancel := context.WithTimeout(r.Context(), tokenCheckTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, gitHubReport{
		App:    h.appStatus(ctx, adapter, repo),
		Gate:   h.gateStatus(ctx, adapter, repo),
		Intake: h.intakeStatus(repo),
	})
}

func (h *settingsHandlers) appStatus(ctx context.Context, adapter gitHubReporter, repo change.Repo) gitHubAppStatus {
	if adapter == nil {
		return gitHubAppStatus{Message: "no GitHub adapter is configured"}
	}
	slug, err := adapter.AppCheck(ctx)
	switch {
	case errors.Is(err, host.ErrNoApp):
		return gitHubAppStatus{Message: "No GitHub App is configured: set " + host.GitHubAppIDKey + " and " + host.GitHubAppKeyKey + " in Settings. Without one no check run is posted and the pull-request face uses the token"}
	case err != nil:
		return gitHubAppStatus{Configured: true, Message: "App check failed: " + redactSecrets(err.Error(), h.secrets.Values())}
	}
	st := gitHubAppStatus{Configured: true, Slug: slug, InstallURL: "https://github.com/apps/" + slug + "/installations/new"}
	installed, err := adapter.AppInstalled(ctx, repo)
	switch {
	case err != nil:
		st.Message = "App " + slug + ": the installation lookup failed: " + redactSecrets(err.Error(), h.secrets.Values())
	case installed:
		st.Installed = true
		st.Message = "App " + slug + " is installed on this repository; check runs are posted as it"
	default:
		st.Message = "App " + slug + " is not installed on this repository: no check run can be posted until it is. Install it at " + st.InstallURL
	}
	return st
}

func (h *settingsHandlers) gateStatus(ctx context.Context, adapter gitHubReporter, repo change.Repo) []gitHubGateStatus {
	out := make([]gitHubGateStatus, 0, len(repo.ProtectedBranches))
	for _, branch := range repo.ProtectedBranches {
		st := gitHubGateStatus{Branch: branch}
		if adapter == nil {
			st.Message = "Could not read the rules of " + branch + ": no GitHub adapter is configured"
			out = append(out, st)
			continue
		}
		contexts, known, err := adapter.RequiredChecks(ctx, repo, branch)
		switch {
		case err != nil:
			st.Message = "Could not read the rules of " + branch + ": " + redactSecrets(err.Error(), h.secrets.Values())
		case !known:
			st.Message = "Whether " + gateCheck + " is required on " + branch + " is unknown: reading the branch protection needs administration read on the token or the App"
		default:
			st.Known = true
			for _, c := range contexts {
				if c == gateCheck {
					st.Required = true
				}
			}
			if st.Required {
				st.Message = gateCheck + " is required on " + branch + ": a merge past a red or waiting gate needs a bypass, which Plumbix records as gate_bypassed"
			} else {
				st.Message = gateCheck + " is not required on " + branch + ": GitHub merges without waiting for the gate, and Plumbix records such merges with reason not_required. Require it in a ruleset or in the branch protection"
			}
		}
		out = append(out, st)
	}
	return out
}

func (h *settingsHandlers) intakeStatus(repo change.Repo) gitHubIntakeStatus {
	_, secretSet := h.secrets.Get(host.GitHubWebhookSecretKey)
	st := gitHubIntakeStatus{WebhookSecretSet: secretSet}
	polling := ""
	if h.intake.PollInterval > 0 {
		st.PollInterval = h.intake.PollInterval.String()
		polling = "polling every " + st.PollInterval
	}
	switch {
	case secretSet:
		st.Mode = "webhook"
		last := "none since the server started"
		if h.intake.LastDelivery != nil {
			if at, ok := h.intake.LastDelivery(repo.ID); ok {
				at = at.UTC()
				st.LastDeliveryAt = &at
				last = "at " + at.Format(time.RFC3339)
			}
		}
		st.Message = "Webhook secret set: signed deliveries are accepted at POST /api/v1/hooks/github; last delivery for this repository " + last
		if polling != "" {
			st.Message += ", and " + polling + " as well"
		} else {
			st.Message += "; polling is off"
		}
	case polling != "":
		st.Mode = "polling"
		st.Message = fmt.Sprintf("Pull requests are found by %s; no webhook secret is set, so deliveries are refused", polling)
	default:
		st.Mode = "none"
		st.Message = "No intake: polling is off and no webhook secret is set, so pull requests are not watched"
	}
	return st
}
