package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// The GitHub REST calls the pull-request face needs (WU-054). Types carry
// GitHub's own field names, so a decoded pull request or an encoded check
// run is the wire shape.

// Actor is a GitHub user or App.
type Actor struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User, Bot, Organization
}

// Ref is one side of a pull request.
type Ref struct {
	Branch string `json:"ref"`
	SHA    string `json:"sha"`
	Repo   struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// PullRequest is what GitHub says about one pull request. Merged, MergedBy
// and Commits come only from a single-PR read.
type PullRequest struct {
	Number         int        `json:"number"`
	URL            string     `json:"html_url"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	State          string     `json:"state"` // open, closed
	Draft          bool       `json:"draft"`
	Author         Actor      `json:"user"`
	Head           Ref        `json:"head"`
	Base           Ref        `json:"base"`
	Merged         bool       `json:"merged"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergedBy       *Actor     `json:"merged_by"`
	Commits        int        `json:"commits"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Review is one pull-request review.
type Review struct {
	ID     int64  `json:"id"`
	Author Actor  `json:"user"`
	Body   string `json:"body"`
	State  string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	// AuthorAssociation is the reviewer's relation to the repository as
	// GitHub states it: OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, NONE and
	// a few more. Only the first three may approve a change (WU-054).
	AuthorAssociation string    `json:"author_association"`
	CommitSHA         string    `json:"commit_id"`
	SubmittedAt       time.Time `json:"submitted_at"`
}

// MayApprove reports whether the review's author has a standing GitHub
// counts for approvals: an owner, an organisation member or a collaborator.
// Anyone with read access can submit an approving review, and on a public
// repository that is anyone.
func (r Review) MayApprove() bool {
	switch r.AuthorAssociation {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// CheckRun is the Checks API shape for creating or updating a check run.
// Empty fields are left out of the request.
type CheckRun struct {
	Name        string       `json:"name,omitempty"`
	HeadSHA     string       `json:"head_sha,omitempty"`
	Status      string       `json:"status,omitempty"`     // queued, in_progress, completed
	Conclusion  string       `json:"conclusion,omitempty"` // success, failure, neutral, cancelled, action_required, timed_out
	ExternalID  string       `json:"external_id,omitempty"`
	DetailsURL  string       `json:"details_url,omitempty"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	CompletedAt *time.Time   `json:"completed_at,omitempty"`
	Output      *CheckOutput `json:"output,omitempty"`
}

// CheckOutput is a check run's report.
type CheckOutput struct {
	Title       string       `json:"title"`
	Summary     string       `json:"summary"`
	Text        string       `json:"text,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Annotation marks a line of a file in a check run.
type Annotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Level     string `json:"annotation_level"` // notice, warning, failure
	Message   string `json:"message"`
	Title     string `json:"title,omitempty"`
}

var ownerRepoRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// ownerRepo is the "owner/name" of a GitHub repository URL.
func ownerRepo(url string) (string, error) {
	m := ownerRepoRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", fmt.Errorf("%s is not a GitHub repository URL", url)
	}
	return m[1] + "/" + m[2], nil
}

// maxPullRequestPages bounds a listing: a hundred per page, so a thousand
// pull requests in one state.
const maxPullRequestPages = 10

// ListPullRequests lists the repository's pull requests in a state (open
// or closed), following pagination up to maxPullRequestPages.
func (g *GitHub) ListPullRequests(ctx context.Context, repo change.Repo, state string) ([]PullRequest, error) {
	var all []PullRequest
	for page := 1; page <= maxPullRequestPages; page++ {
		var prs []PullRequest
		if err := g.rest(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls?state=%s&per_page=100&page=%d", state, page), nil, &prs); err != nil {
			return nil, err
		}
		all = append(all, prs...)
		if len(prs) < 100 {
			break
		}
	}
	return all, nil
}

// GetPullRequest reads one pull request, merge details included.
func (g *GitHub) GetPullRequest(ctx context.Context, repo change.Repo, number int) (PullRequest, error) {
	var pr PullRequest
	err := g.rest(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d", number), nil, &pr)
	return pr, err
}

// ListReviews lists a pull request's reviews, oldest first, following
// pagination up to maxPullRequestPages.
func (g *GitHub) ListReviews(ctx context.Context, repo change.Repo, number int) ([]Review, error) {
	var all []Review
	for page := 1; page <= maxPullRequestPages; page++ {
		var reviews []Review
		if err := g.rest(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews?per_page=100&page=%d", number, page), nil, &reviews); err != nil {
			return nil, err
		}
		all = append(all, reviews...)
		if len(reviews) < 100 {
			break
		}
	}
	return all, nil
}

// CreateCheckRun creates a check run and returns its id.
func (g *GitHub) CreateCheckRun(ctx context.Context, repo change.Repo, c CheckRun) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := g.rest(ctx, repo, http.MethodPost, "/check-runs", c, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// UpdateCheckRun changes a check run.
func (g *GitHub) UpdateCheckRun(ctx context.Context, repo change.Repo, id int64, c CheckRun) error {
	return g.rest(ctx, repo, http.MethodPatch, fmt.Sprintf("/check-runs/%d", id), c, nil)
}

// RequiredChecks lists the status-check contexts a branch requires before a
// pull request may merge, from the repository's rulesets and then from
// classic branch protection. known is false when GitHub would not say (the
// protection endpoint needs administration read); an unprotected branch
// is known to require nothing.
func (g *GitHub) RequiredChecks(ctx context.Context, repo change.Repo, branch string) (contexts []string, known bool, err error) {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := g.rest(ctx, repo, http.MethodGet, "/rules/branches/"+url.PathEscape(branch), nil, &rules); err != nil && !isStatus(err, http.StatusNotFound, http.StatusForbidden) {
		return nil, false, err
	}
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, c := range r.Parameters.Checks {
			contexts = append(contexts, c.Context)
		}
	}
	if len(contexts) > 0 {
		return contexts, true, nil
	}
	var classic struct {
		Contexts []string `json:"contexts"`
	}
	err = g.rest(ctx, repo, http.MethodGet, "/branches/"+url.PathEscape(branch)+"/protection/required_status_checks", nil, &classic)
	switch {
	case err == nil:
		return classic.Contexts, true, nil
	case isStatus(err, http.StatusNotFound):
		// Not protected, or no required checks: nothing is required.
		return nil, true, nil
	case isStatus(err, http.StatusForbidden):
		return nil, false, nil
	}
	return nil, false, err
}

// StatusError is a GitHub answer with an unexpected status.
type StatusError struct {
	Status int
	Path   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub %s: status %d", e.Path, e.Status)
}

func isStatus(err error, statuses ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, s := range statuses {
		if se.Status == s {
			return true
		}
	}
	return false
}

// apiToken is the token for API calls on a repository: the App's
// installation token when an App is configured and installed on the
// repository, since only an App may create check runs (WU-055); else the
// repository's own token or the host token. With neither, the error says
// what failed.
func (g *GitHub) apiToken(ctx context.Context, repo change.Repo) (string, error) {
	token := tokenFor(repo, g.Token, g.RepoTokens)
	if g.App == nil {
		if token == "" {
			return "", fmt.Errorf("no GitHub token for repository %s and no App configured", repo.ID)
		}
		return token, nil
	}
	or, err := ownerRepo(repo.URL)
	if err != nil {
		return "", err
	}
	inst, err := g.App.InstallationToken(ctx, g.base(), or)
	if err != nil {
		if token == "" {
			return "", fmt.Errorf("no GitHub token for repository %s and the App minted none: %w", repo.ID, err)
		}
		return token, nil
	}
	return inst, nil
}

// AppCheck verifies the configured App's id and key against GitHub,
// whatever token is set beside it (WU-055).
func (g *GitHub) AppCheck(ctx context.Context) error {
	if g.AppErr != nil {
		return fmt.Errorf("GitHub App: %w", g.AppErr)
	}
	if g.App == nil {
		return errors.New("no GitHub App configured: set " + GitHubAppIDKey + " and " + GitHubAppKeyKey)
	}
	return g.App.Check(ctx, g.base())
}

func (g *GitHub) base() string {
	if g.APIBase == "" {
		return gitHubAPI
	}
	return g.APIBase
}

// rest makes one authenticated call under /repos/{owner}/{repo}.
func (g *GitHub) rest(ctx context.Context, repo change.Repo, method, path string, in, out any) error {
	or, err := ownerRepo(repo.URL)
	if err != nil {
		return err
	}
	tok, err := g.apiToken(ctx, repo)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base()+"/repos/"+or+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVer)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Status: resp.StatusCode, Path: method + " " + path}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub %s %s: decode: %w", method, path, err)
	}
	return nil
}
