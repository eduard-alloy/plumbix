package change

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
)

// Limits on submitted text, to keep requests and storage bounded.
const (
	MaxTitle       = 200
	MaxIntent      = 20000
	MaxText        = 2000
	MaxCriteria    = 50
	MaxConstraints = 50
	MaxSubmittedBy = 100
	maxBranch      = 255
)

// Hosts and merge strategies Plumbix supports.
const (
	HostGitHub      = "github"
	HostAzureDevOps = "azuredevops"
	HostGit         = "git" // plain git: local path, file:// or SSH
	DefaultBranch   = "main"
	DefaultStrategy = "squash"
)

var mergeStrategies = map[string]bool{"squash": true, "merge": true, "rebase": true}

var (
	githubURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	azureURLs = []*regexp.Regexp{
		regexp.MustCompile(`^https://dev\.azure\.com/[^/\s]+/[^/\s]+/_git/[^/\s]+$`),
		regexp.MustCompile(`^https://[A-Za-z0-9-]+\.visualstudio\.com/[^/\s]+/_git/[^/\s]+$`),
	}
	criterionID = regexp.MustCompile(`^AC-[1-9][0-9]*$`)
)

// Problem is one invalid field in a request.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// InvalidError lists every problem found in a request.
type InvalidError struct {
	Problems []Problem
}

func (e *InvalidError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Message
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

type problems []Problem

func (ps *problems) add(field, format string, args ...any) {
	*ps = append(*ps, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (ps problems) err() error {
	if len(ps) == 0 {
		return nil
	}
	return &InvalidError{Problems: ps}
}

// RepoInput is a repo registration request.
type RepoInput struct {
	Host              string   `json:"host"`
	URL               string   `json:"url"`
	ProtectedBranches []string `json:"protected_branches"`
	MergeStrategy     string   `json:"merge_strategy"`
}

// ChangeInput is a change submission request.
type ChangeInput struct {
	RepoID             string      `json:"repo_id"`
	SourceBranch       string      `json:"source_branch"`
	TargetBranch       string      `json:"target_branch"`
	Title              string      `json:"title"`
	Intent             string      `json:"intent"`
	AcceptanceCriteria []Criterion `json:"acceptance_criteria"`
	Constraints        []string    `json:"constraints"`
	SubmittedBy        string      `json:"submitted_by"`
}

// normalizeRepo fills defaults and checks every rule.
func normalizeRepo(in RepoInput) (RepoInput, error) {
	var ps problems
	switch in.Host {
	case HostGitHub:
		if !githubURL.MatchString(in.URL) {
			ps.add("url", "must look like https://github.com/<owner>/<repo>")
		}
	case HostAzureDevOps:
		if !azureURLs[0].MatchString(in.URL) && !azureURLs[1].MatchString(in.URL) {
			ps.add("url", "must look like https://dev.azure.com/<org>/<project>/_git/<repo> or https://<org>.visualstudio.com/<project>/_git/<repo>")
		}
	case HostGit:
		if msg := plainRemoteProblem(in.URL); msg != "" {
			ps.add("url", "%s", msg)
		}
	case "":
		ps.add("host", "is required")
	default:
		ps.add("host", "must be %q, %q or %q", HostGitHub, HostAzureDevOps, HostGit)
	}
	if in.MergeStrategy == "" {
		in.MergeStrategy = DefaultStrategy
	} else if !mergeStrategies[in.MergeStrategy] {
		ps.add("merge_strategy", "must be squash, merge or rebase")
	}
	if len(in.ProtectedBranches) == 0 {
		in.ProtectedBranches = []string{DefaultBranch}
	}
	seen := map[string]bool{}
	for i, b := range in.ProtectedBranches {
		field := fmt.Sprintf("protected_branches[%d]", i)
		switch {
		case !ValidBranch(b):
			ps.add(field, "is not a valid branch name")
		case seen[b]:
			ps.add(field, "duplicate branch %q", b)
		}
		seen[b] = true
	}
	return in, ps.err()
}

// normalizeChange trims, assigns criterion IDs and checks every rule.
func normalizeChange(in ChangeInput) (ChangeInput, error) {
	var ps problems
	in.Title, in.Intent, in.SubmittedBy = strings.TrimSpace(in.Title), strings.TrimSpace(in.Intent), strings.TrimSpace(in.SubmittedBy)
	if in.RepoID == "" {
		ps.add("repo_id", "is required")
	}
	branch := func(field, value string) {
		switch {
		case value == "":
			ps.add(field, "is required")
		case !ValidBranch(value):
			ps.add(field, "is not a valid branch name")
		}
	}
	branch("source_branch", in.SourceBranch)
	branch("target_branch", in.TargetBranch)
	if in.SourceBranch != "" && in.SourceBranch == in.TargetBranch {
		ps.add("target_branch", "must differ from source_branch")
	}
	text := func(field, value string, max int) {
		switch {
		case value == "":
			ps.add(field, "is required")
		case utf8.RuneCountInString(value) > max:
			ps.add(field, "must be at most %d characters", max)
		}
	}
	text("title", in.Title, MaxTitle)
	text("intent", in.Intent, MaxIntent)
	if utf8.RuneCountInString(in.SubmittedBy) > MaxSubmittedBy {
		ps.add("submitted_by", "must be at most %d characters", MaxSubmittedBy)
	}

	switch n := len(in.AcceptanceCriteria); {
	case n == 0:
		ps.add("acceptance_criteria", "must contain at least one criterion")
	case n > MaxCriteria:
		ps.add("acceptance_criteria", "must contain at most %d criteria", MaxCriteria)
	}
	criteria := make([]Criterion, len(in.AcceptanceCriteria))
	used := map[string]bool{}
	for i, c := range in.AcceptanceCriteria {
		field := fmt.Sprintf("acceptance_criteria[%d]", i)
		c.Text = strings.TrimSpace(c.Text)
		text(field+".text", c.Text, MaxText)
		if c.ID != "" {
			switch {
			case !criterionID.MatchString(c.ID):
				ps.add(field+".id", "must look like AC-<number>")
			case used[c.ID]:
				ps.add(field+".id", "duplicate id %q", c.ID)
			}
			used[c.ID] = true
		}
		if c.Check != nil {
			checkProblems(&ps, field+".check", c.Check)
		}
		criteria[i] = c
	}
	next := 1
	for i := range criteria {
		if criteria[i].ID != "" {
			continue
		}
		for used["AC-"+strconv.Itoa(next)] {
			next++
		}
		criteria[i].ID = "AC-" + strconv.Itoa(next)
		used[criteria[i].ID] = true
	}
	in.AcceptanceCriteria = criteria

	if len(in.Constraints) > MaxConstraints {
		ps.add("constraints", "must contain at most %d items", MaxConstraints)
	}
	constraints := make([]string, 0, len(in.Constraints))
	for i, c := range in.Constraints {
		c = strings.TrimSpace(c)
		text(fmt.Sprintf("constraints[%d]", i), c, MaxText)
		constraints = append(constraints, c)
	}
	in.Constraints = constraints
	return in, ps.err()
}

func checkProblems(ps *problems, field string, c *Check) {
	hasSteps, hasCommand := len(c.Steps) > 0, strings.TrimSpace(c.Command) != ""
	switch {
	case hasSteps && hasCommand:
		ps.add(field, "must have either steps or command, not both")
	case !hasSteps && !hasCommand:
		ps.add(field, "must have steps or command")
	case hasCommand && utf8.RuneCountInString(c.Command) > MaxText:
		ps.add(field+".command", "must be at most %d characters", MaxText)
	}
	for j, s := range c.Steps {
		if !pipeline.ValidStepName(s) {
			ps.add(fmt.Sprintf("%s.steps[%d]", field, j), "is not a valid step name")
		}
	}
}

var (
	sshURL = regexp.MustCompile(`^ssh://([A-Za-z0-9._-]+@)?[A-Za-z0-9.-]+(:[0-9]+)?/.+$`)
	scpURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/\s].*$`)
)

// plainRemoteProblem explains what is wrong with a plain git remote, or
// returns "" when it is acceptable. Local paths must be git repositories.
func plainRemoteProblem(remote string) string {
	switch {
	case remote == "":
		return "is required"
	case strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://"):
		return `HTTP remotes use host "github" or "azuredevops"; plain git accepts an absolute path, file://, ssh:// or user@host:path`
	case sshURL.MatchString(remote) || scpURL.MatchString(remote):
		return ""
	}
	path := remote
	if p, ok := strings.CutPrefix(remote, "file://"); ok {
		path = p
	}
	if !filepath.IsAbs(path) {
		return "must be an absolute path, file:///absolute/path, ssh:// or user@host:path"
	}
	if !isGitRepo(path) {
		return path + " is not a git repository"
	}
	return ""
}

// isGitRepo reports whether path is a bare repository or a working tree.
func isGitRepo(path string) bool {
	for _, marker := range []string{"HEAD", filepath.Join(".git", "HEAD")} {
		if info, err := os.Stat(filepath.Join(path, marker)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// ValidBranch applies git's rules for branch names (git check-ref-format).
func ValidBranch(name string) bool {
	if name == "" || len(name) > maxBranch || name == "@" || strings.HasPrefix(name, "-") {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
