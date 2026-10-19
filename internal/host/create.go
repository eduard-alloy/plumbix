package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// azureDevOpsAPI is where Azure DevOps organisations live.
const (
	azureDevOpsAPI    = "https://dev.azure.com"
	azureDevOpsAPIVer = "7.1"
)

// errNoToken says no token can create the repository.
var errNoToken = errors.New("no token: set the host token in Settings or give one for this repository")

// CreateRepository creates an empty repository on GitHub for publishing a
// local folder (WU-042, A35), under the token's account when owner is empty
// or that account's login, else under the organisation owner. token, when
// given, wins over the host token. It returns the repository's HTTPS URL.
// The token travels only in the Authorization header.
func (g *GitHub) CreateRepository(ctx context.Context, token, owner, name string, private bool) (string, error) {
	if token == "" {
		token = g.Token
	}
	if token == "" {
		return "", errNoToken
	}
	base := g.APIBase
	if base == "" {
		base = gitHubAPI
	}
	call := func(method, path string, body any, out any) (int, string, error) {
		var rd io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			rd = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", gitHubAPIVer)
		resp, err := apiClient.Do(req)
		if err != nil {
			return 0, "", fmt.Errorf("reach GitHub: %w", err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 == 2 && out != nil {
			_ = json.Unmarshal(data, out)
		}
		return resp.StatusCode, gitHubMessage(data), nil
	}
	var me struct {
		Login string `json:"login"`
	}
	code, msg, err := call(http.MethodGet, "/user", nil, &me)
	if err != nil {
		return "", err
	}
	if code == http.StatusUnauthorized {
		return "", errors.New("GitHub rejected the token")
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("read the token's account: GitHub answered %d %s", code, msg)
	}
	path := "/user/repos"
	if owner != "" && !strings.EqualFold(owner, me.Login) {
		path = "/orgs/" + url.PathEscape(owner) + "/repos"
	}
	var created struct {
		FullName string `json:"full_name"`
	}
	code, msg, err = call(http.MethodPost, path, map[string]any{"name": name, "private": private, "auto_init": false}, &created)
	if err != nil {
		return "", err
	}
	switch {
	case code == http.StatusCreated && created.FullName != "":
		return "https://github.com/" + created.FullName, nil
	case code == http.StatusUnauthorized:
		return "", errors.New("GitHub rejected the token")
	case code == http.StatusForbidden || code == http.StatusNotFound:
		return "", fmt.Errorf("GitHub refused to create the repository (%d): the token needs permission to create repositories for %s. %s", code, ownerOr(owner, me.Login), msg)
	}
	return "", fmt.Errorf("GitHub refused to create the repository (%d): %s", code, msg)
}

func ownerOr(owner, login string) string {
	if owner != "" {
		return owner
	}
	return login
}

// gitHubMessage is the readable part of a GitHub error body.
func gitHubMessage(data []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	parts := []string{e.Message}
	for _, x := range e.Errors {
		if x.Message != "" {
			parts = append(parts, x.Message)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// CreateRepository creates an empty repository in an Azure DevOps project
// for publishing a local folder (WU-042). token, when given, wins over the
// host token; it travels as basic auth. It returns the repository's HTTPS
// URL in the form Plumbix registers.
func (a *AzureDevOps) CreateRepository(ctx context.Context, token, org, project, name string) (string, error) {
	if token == "" {
		token = a.Token
	}
	if token == "" {
		return "", errNoToken
	}
	if org == "" || project == "" || name == "" {
		return "", errors.New("an organisation, a project and a name are required")
	}
	base := a.APIBase
	if base == "" {
		base = azureDevOpsAPI
	}
	data, _ := json.Marshal(map[string]string{"name": name})
	endpoint := fmt.Sprintf("%s/%s/%s/_apis/git/repositories?api-version=%s", base, url.PathEscape(org), url.PathEscape(project), azureDevOpsAPIVer)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach Azure DevOps: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusNonAuthoritativeInfo, http.StatusUnauthorized:
		// Azure DevOps answers a bad token with a sign-in page (203).
		return "", errors.New("Azure DevOps rejected the token")
	default:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		return "", fmt.Errorf("Azure DevOps refused to create the repository (%d): %s", resp.StatusCode, e.Message)
	}
	var created struct {
		RemoteURL string `json:"remoteUrl"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.RemoteURL == "" {
		return "", errors.New("Azure DevOps created the repository but sent no URL")
	}
	_, u, err := change.DetectRemote(created.RemoteURL)
	if err != nil {
		return "", err
	}
	return u, nil
}
