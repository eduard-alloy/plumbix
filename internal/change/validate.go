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
	// PullRequests opts a GitHub repository into pull-request intake; Merger
	// is plumbix or github and defaults to github when PullRequests is set
	// (WU-054).
	PullRequests bool   `json:"pull_requests"`
	Merger       string `json:"merger"`
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
	// PlumbixBranch is set by the server for branches it created; a client
	// cannot claim it.
	PlumbixBranch bool `json:"-"`
	// PullRequest is set by the server for a change it opened from a GitHub
	// pull request (WU-054); a client cannot claim it. AllowNoCriteria lets
	// such a change state no criteria when the pull request stated none,
	// rather than inventing one.
	PullRequest     *PullRequestRef `json:"-"`
	AllowNoCriteria bool            `json:"-"`
	// CriteriaSet names the set to take the criteria from (WU-066); the
	// submission's own acceptance_criteria are then ignored. CriteriaSetRef
	// is set by the server when a pull request named a set it could not
	// use, with the problem; a client cannot claim it.
	CriteriaSet    string          `json:"criteria_set"`
	CriteriaSetRef *CriteriaSetRef `json:"-"`
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
	in.Merger = normalizeMerging(in.Host, in.PullRequests, in.Merger, &ps)
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

	in.CriteriaSet = strings.TrimSpace(in.CriteriaSet)
	if in.CriteriaSet != "" && !criteriaSetID.MatchString(in.CriteriaSet) {
		ps.add("criteria_set", "must look like cs_<id>")
	}
	// A named set supplies the criteria; the submission's own list is not
	// required then (WU-066).
	in.AcceptanceCriteria = normalizeCriteria(&ps, "acceptance_criteria", in.AcceptanceCriteria, in.AllowNoCriteria || in.CriteriaSet != "", true)

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

var criteriaSetID = regexp.MustCompile(`^cs_[a-z0-9]{6,32}$`)

// normalizeCriteria trims, bounds and ids a list of criteria, the same
// way for a submission and for a criteria set (WU-066). commands says
// whether a check may be a command: a submission may, a set may not.
func normalizeCriteria(ps *problems, field string, in []Criterion, allowEmpty, commands bool) []Criterion {
	switch n := len(in); {
	case n == 0 && !allowEmpty:
		ps.add(field, "must contain at least one criterion")
	case n > MaxCriteria:
		ps.add(field, "must contain at most %d criteria", MaxCriteria)
	}
	criteria := make([]Criterion, len(in))
	used := map[string]bool{}
	for i, c := range in {
		item := fmt.Sprintf("%s[%d]", field, i)
		c.Text = strings.TrimSpace(c.Text)
		switch {
		case c.Text == "":
			ps.add(item+".text", "is required")
		case utf8.RuneCountInString(c.Text) > MaxText:
			ps.add(item+".text", "must be at most %d characters", MaxText)
		}
		if c.ID != "" {
			switch {
			case !criterionID.MatchString(c.ID):
				ps.add(item+".id", "must look like AC-<number>")
			case used[c.ID]:
				ps.add(item+".id", "duplicate id %q", c.ID)
			}
			used[c.ID] = true
		}
		if c.Check != nil {
			checkProblems(ps, item+".check", c.Check)
			if !commands && strings.TrimSpace(c.Check.Command) != "" {
				ps.add(item+".check.command", "is not allowed in a criteria set: a set binds steps only, since any change may name it")
			}
		}
		c.Note = strings.TrimSpace(c.Note)
		if utf8.RuneCountInString(c.Note) > MaxText {
			ps.add(item+".note", "must be at most %d characters", MaxText)
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
	return criteria
}

// normalizeCriteriaSet trims and checks a criteria set's input (WU-066).
func normalizeCriteriaSet(in CriteriaSetInput) (CriteriaSetInput, error) {
	var ps problems
	in.Task, in.Title, in.Owner = strings.TrimSpace(in.Task), strings.TrimSpace(in.Title), strings.TrimSpace(in.Owner)
	switch {
	case in.Task == "":
		ps.add("task", "is required: the specification the criteria belong to, such as kai:<project>/<ref>")
	case utf8.RuneCountInString(in.Task) > MaxTaskRef:
		ps.add("task", "must be at most %d characters", MaxTaskRef)
	}
	// A set's title is the specification's own, however long the tool
	// wrote it; bounded like a criterion's text, not like a change's title.
	if utf8.RuneCountInString(in.Title) > MaxText {
		ps.add("title", "must be at most %d characters", MaxText)
	}
	switch {
	case in.Owner == "":
		ps.add("owner", "is required: the person the criteria belong to")
	case utf8.RuneCountInString(in.Owner) > MaxSubmittedBy:
		ps.add("owner", "must be at most %d characters", MaxSubmittedBy)
	}
	if in.Source != nil {
		src := *in.Source
		src.Kind, src.Project, src.Ref = strings.TrimSpace(src.Kind), strings.TrimSpace(src.Project), strings.TrimSpace(src.Ref)
		if src.Kind == "" {
			ps.add("source.kind", "is required when a source is given")
		}
		for name, v := range map[string]string{"source.kind": src.Kind, "source.project": src.Project, "source.ref": src.Ref} {
			if utf8.RuneCountInString(v) > MaxSubmittedBy {
				ps.add(name, "must be at most %d characters", MaxSubmittedBy)
			}
		}
		in.Source = &src
	}
	for i := range in.Criteria {
		in.Criteria[i].ID = "" // a set numbers its own criteria
		in.Criteria[i].Note = ""
	}
	in.Criteria = normalizeCriteria(&ps, "criteria", in.Criteria, false, false)
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

// normalizeMerging checks a repository's pull_requests and merger against
// its host and returns the merger to store: the given one, or the default
// for the pull_requests value (github with pull requests, plumbix without).
// Registration and the settings update (WU-055) share the rules.
func normalizeMerging(host string, pullRequests bool, merger string, ps *problems) string {
	if pullRequests && host != HostGitHub {
		ps.add("pull_requests", "only GitHub repositories have pull requests Plumbix can watch")
	}
	switch merger {
	case "":
		if pullRequests {
			return MergerGitHub
		}
		return MergerPlumbix
	case MergerPlumbix:
	case MergerGitHub:
		if host != HostGitHub {
			ps.add("merger", "github merges only GitHub repositories")
		}
	default:
		ps.add("merger", "must be plumbix or github")
	}
	return merger
}

// CheckRepoMerging reports whether pullRequests and merger are acceptable
// for a repository on host, without storing anything: what a handler calls
// before it writes any part of a settings update (WU-055).
func CheckRepoMerging(host string, pullRequests bool, merger string) error {
	var ps problems
	normalizeMerging(host, pullRequests, merger, &ps)
	return ps.err()
}
