package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// restServer records the requests a GitHub REST call makes and answers
// from a table by method and path.
type restServer struct {
	*httptest.Server
	answers map[string]string // "METHOD /path" -> body
	pages   map[string]string // body by page query value, for GET /pulls
	status  map[string]int
	got     []recorded
}

type recorded struct {
	Method, Path, Query, Body string
}

func fakeREST(t *testing.T) *restServer {
	t.Helper()
	s := &restServer{answers: map[string]string{}, status: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-GitHub-Api-Version") != gitHubAPIVer {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		// EscapedPath keeps a branch's %2F visible, as GitHub would see it.
		s.got = append(s.got, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(body)})
		key := r.Method + " " + r.URL.EscapedPath()
		if st, ok := s.status[key]; ok {
			w.WriteHeader(st)
		}
		if body, ok := s.pages[r.URL.Query().Get("page")]; ok && r.URL.Path == "/repos/a/b/pulls" {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(s.answers[key]))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestListPullRequestsAndReviews(t *testing.T) {
	srv := fakeREST(t)
	srv.answers["GET /repos/a/b/pulls"] = `[{"number":7,"html_url":"https://github.com/a/b/pull/7","title":"T","body":"B","state":"open","draft":false,
	  "user":{"login":"octocat","type":"User"},"head":{"ref":"feat","sha":"h1","repo":{"full_name":"a/b"}},"base":{"ref":"main","sha":"m1","repo":{"full_name":"a/b"}},
	  "merged_at":null,"merge_commit_sha":"mc","updated_at":"2026-10-28T10:00:00Z"}]`
	srv.answers["GET /repos/a/b/pulls/7"] = `{"number":7,"html_url":"https://github.com/a/b/pull/7","title":"T","body":"B","state":"closed","merged":true,"merged_at":"2026-10-28T11:00:00Z",
	  "merge_commit_sha":"mc","merged_by":{"login":"maintainer","type":"User"},"commits":3,"user":{"login":"dependabot[bot]","type":"Bot"},
	  "head":{"ref":"feat","sha":"h1","repo":{"full_name":"a/b"}},"base":{"ref":"main","sha":"m1","repo":{"full_name":"a/b"}}}`
	srv.answers["GET /repos/a/b/pulls/7/reviews"] = `[{"id":1,"user":{"login":"lead","type":"User"},"body":"ship it","state":"APPROVED","author_association":"COLLABORATOR","commit_id":"h1","submitted_at":"2026-10-28T10:30:00Z"}]`
	g := &GitHub{Token: "tok", APIBase: srv.URL}
	ctx := context.Background()

	prs, err := g.ListPullRequests(ctx, repo, "open")
	if err != nil || len(prs) != 1 || prs[0].Number != 7 || prs[0].Head.Branch != "feat" || prs[0].Head.Repo.FullName != "a/b" || prs[0].Author.Login != "octocat" || prs[0].MergedAt != nil {
		t.Fatalf("prs = %+v, %v", prs, err)
	}
	if q := srv.got[0].Query; q != "state=open&per_page=100&page=1" {
		t.Errorf("query = %s", q)
	}
	pr, err := g.GetPullRequest(ctx, repo, 7)
	if err != nil || !pr.Merged || pr.MergedBy == nil || pr.MergedBy.Login != "maintainer" || pr.Commits != 3 || pr.MergeCommitSHA != "mc" || pr.Author.Type != "Bot" {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	reviews, err := g.ListReviews(ctx, repo, 7)
	if err != nil || len(reviews) != 1 || reviews[0].State != "APPROVED" || reviews[0].CommitSHA != "h1" || reviews[0].Author.Login != "lead" || reviews[0].Body != "ship it" || !reviews[0].MayApprove() {
		t.Fatalf("reviews = %+v, %v", reviews, err)
	}
}

// TestCheckRunCalls: create posts the Checks API shape and returns the id;
// update patches it. The body carries only what is set.
func TestCheckRunCalls(t *testing.T) {
	srv := fakeREST(t)
	srv.answers["POST /repos/a/b/check-runs"] = `{"id":99}`
	srv.status["POST /repos/a/b/check-runs"] = http.StatusCreated
	g := &GitHub{Token: "tok", APIBase: srv.URL}
	ctx := context.Background()
	started := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	id, err := g.CreateCheckRun(ctx, repo, CheckRun{Name: "plumbix/gate", HeadSHA: "h1", Status: "in_progress", StartedAt: &started,
		Output: &CheckOutput{Title: "Validating", Summary: "run_1 started"}})
	if err != nil || id != 99 {
		t.Fatalf("create = %d, %v", id, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(srv.got[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "plumbix/gate" || body["head_sha"] != "h1" || body["status"] != "in_progress" || body["started_at"] != "2026-10-28T10:00:00Z" {
		t.Errorf("create body = %v", body)
	}
	if _, has := body["conclusion"]; has {
		t.Errorf("empty conclusion sent: %v", body)
	}
	if out := body["output"].(map[string]any); out["title"] != "Validating" || out["summary"] != "run_1 started" {
		t.Errorf("output = %v", out)
	}
	if _, has := body["output"].(map[string]any)["annotations"]; has {
		t.Errorf("empty annotations sent: %v", body)
	}

	if err := g.UpdateCheckRun(ctx, repo, 99, CheckRun{Status: "completed", Conclusion: "success", Output: &CheckOutput{Title: "Approved", Summary: "s", Text: "t",
		Annotations: []Annotation{{Path: "a.go", StartLine: 3, EndLine: 3, Level: "warning", Message: "m"}}}}); err != nil {
		t.Fatal(err)
	}
	if r := srv.got[1]; r.Method != http.MethodPatch || r.Path != "/repos/a/b/check-runs/99" {
		t.Errorf("update = %+v", r)
	}
	body = nil // Unmarshal merges into a map; start clean
	if err := json.Unmarshal([]byte(srv.got[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	ann := body["output"].(map[string]any)["annotations"].([]any)[0].(map[string]any)
	if body["conclusion"] != "success" || ann["path"] != "a.go" || ann["start_line"] != float64(3) || ann["annotation_level"] != "warning" {
		t.Errorf("update body = %v", body)
	}
	if _, has := body["name"]; has {
		t.Errorf("update sent an empty name: %v", body)
	}
}

// TestRequiredChecks: the rules endpoint names required contexts; the
// classic protection endpoint is the fallback; neither readable means
// unknown, not "not required".
func TestRequiredChecks(t *testing.T) {
	ctx := context.Background()
	srv := fakeREST(t)
	srv.answers["GET /repos/a/b/rules/branches/main"] = `[{"type":"deletion"},{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"plumbix/gate"},{"context":"ci"}]}}]`
	g := &GitHub{Token: "tok", APIBase: srv.URL}
	contexts, known, err := g.RequiredChecks(ctx, repo, "main")
	if err != nil || !known || len(contexts) != 2 || contexts[0] != "plumbix/gate" {
		t.Fatalf("rules: %v, %v, %v", contexts, known, err)
	}
	srv.answers["GET /repos/a/b/rules/branches/release%2F2026"] = `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"plumbix/gate"}]}}]`
	if contexts, known, err := g.RequiredChecks(ctx, repo, "release/2026"); err != nil || !known || len(contexts) != 1 {
		t.Fatalf("branch with a slash: %v, %v, %v (requests %+v)", contexts, known, err, srv.got)
	}

	classic := fakeREST(t)
	classic.answers["GET /repos/a/b/rules/branches/main"] = `[]`
	classic.answers["GET /repos/a/b/branches/main/protection/required_status_checks"] = `{"contexts":["plumbix/gate"],"checks":[{"context":"plumbix/gate","app_id":1}]}`
	g = &GitHub{Token: "tok", APIBase: classic.URL}
	contexts, known, err = g.RequiredChecks(ctx, repo, "main")
	if err != nil || !known || len(contexts) != 1 || contexts[0] != "plumbix/gate" {
		t.Fatalf("classic: %v, %v, %v", contexts, known, err)
	}

	hidden := fakeREST(t)
	hidden.answers["GET /repos/a/b/rules/branches/main"] = `[]`
	hidden.status["GET /repos/a/b/branches/main/protection/required_status_checks"] = http.StatusForbidden
	g = &GitHub{Token: "tok", APIBase: hidden.URL}
	if contexts, known, err := g.RequiredChecks(ctx, repo, "main"); err != nil || known || len(contexts) != 0 {
		t.Fatalf("hidden: %v, %v, %v", contexts, known, err)
	}

	unprotected := fakeREST(t)
	unprotected.answers["GET /repos/a/b/rules/branches/main"] = `[]`
	unprotected.status["GET /repos/a/b/branches/main/protection/required_status_checks"] = http.StatusNotFound
	g = &GitHub{Token: "tok", APIBase: unprotected.URL}
	if contexts, known, err := g.RequiredChecks(ctx, repo, "main"); err != nil || !known || len(contexts) != 0 {
		t.Fatalf("unprotected: %v, %v, %v", contexts, known, err)
	}
}

func TestOwnerRepo(t *testing.T) {
	for in, want := range map[string]string{"https://github.com/a/b": "a/b", "https://github.com/a/b.git": "a/b", "https://github.com/a/b/": "a/b"} {
		if got, err := ownerRepo(in); err != nil || got != want {
			t.Errorf("ownerRepo(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ownerRepo("https://dev.azure.com/o/p/_git/r"); err == nil {
		t.Error("non-GitHub URL accepted")
	}
}

// TestListPullRequestsFollowsPages: a full page means another page.
func TestListPullRequestsFollowsPages(t *testing.T) {
	srv := fakeREST(t)
	full := make([]string, 100)
	for i := range full {
		full[i] = fmt.Sprintf(`{"number":%d,"state":"open","head":{"ref":"b%d","repo":{"full_name":"a/b"}},"base":{"ref":"main","repo":{"full_name":"a/b"}}}`, i+1, i+1)
	}
	srv.pages = map[string]string{"1": "[" + strings.Join(full, ",") + "]", "2": `[{"number":101,"state":"open","head":{"ref":"b101","repo":{"full_name":"a/b"}},"base":{"ref":"main","repo":{"full_name":"a/b"}}}]`}
	prs, err := (&GitHub{Token: "tok", APIBase: srv.URL}).ListPullRequests(context.Background(), repo, "open")
	if err != nil || len(prs) != 101 || prs[100].Number != 101 {
		t.Fatalf("prs = %d, %v", len(prs), err)
	}
}
