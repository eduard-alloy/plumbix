package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/importer"
)

// MaxImportFile is the largest file the importer reads.
const MaxImportFile = 1 << 20

// ErrUpToDate means an import would write nothing (WU-048).
var ErrUpToDate = errors.New("the imported pipelines are up to date: a re-check would change nothing")

// ImportView is a preview of importing a branch head's pipelines (WU-046):
// the conversion and, over the pipelines already there, the plan (WU-048).
type ImportView struct {
	Branch string
	SHA    string
	Result importer.Result
	Plan   importer.Plan
}

// ImportPreview converts the source system's pipelines at a branch head and
// plans them over the imported pipelines already there. It reads YAML files
// only and writes nothing. Templates from other repositories are read from
// registered ones (WU-047), each fetched once.
func (e *Engine) ImportPreview(ctx context.Context, repoID, branch string, src importer.Source) (ImportView, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return ImportView{}, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return ImportView{}, err
	}
	return e.importView(ctx, repo, m, branch, src)
}

func (e *Engine) importView(ctx context.Context, repo change.Repo, m *gitops.Mirror, branch string, src importer.Source) (ImportView, error) {
	head, err := m.Head(ctx, branch)
	if err != nil {
		return ImportView{}, err
	}
	files, big, err := yamlFiles(ctx, m, head)
	if err != nil {
		return ImportView{}, err
	}
	r := importer.Preview(src, importer.Lookup{Files: files, Repos: e.importRepos(ctx, repo)})
	r.Problems = append(big, r.Problems...)
	existing := map[string][]byte{}
	for p, c := range files {
		if strings.HasPrefix(p, importer.Dir) {
			existing[p] = c
		}
	}
	return ImportView{Branch: branch, SHA: head, Result: r, Plan: importer.Recheck(src.Name(), r, existing)}, nil
}

// ImportRequest says where an import is proposed (WU-048).
type ImportRequest struct {
	TargetBranch string // read, and built on
	Branch       string // the new branch; empty means plumbix/import-<head>
	Message      string
	// RecheckMessage, when set, is the commit message instead of Message
	// once any file was imported before.
	RecheckMessage string
}

// ImportProposal is an import pushed as a new branch.
type ImportProposal struct {
	ImportView
	Branch  string
	Commit  string
	Message string // the commit message used
}

// ImportPropose plans an import on the target branch head and commits every
// file it writes in one commit, pushed as a new branch with the
// repository's credentials (WU-048, A37). The caller opens the change.
// ErrUpToDate when there is nothing to write.
func (e *Engine) ImportPropose(ctx context.Context, repoID string, req ImportRequest, src importer.Source) (ImportProposal, error) {
	repo, err := e.store.GetRepo(ctx, repoID)
	if err != nil {
		return ImportProposal{}, err
	}
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return ImportProposal{}, err
	}
	v, err := e.importView(ctx, repo, m, req.TargetBranch, src)
	if err != nil {
		return ImportProposal{}, err
	}
	if !v.Plan.Changed() {
		return ImportProposal{}, ErrUpToDate
	}
	branch := req.Branch
	if branch == "" {
		branch = "plumbix/import-" + short(v.SHA)
	}
	if !change.ValidBranch(branch) {
		return ImportProposal{}, &change.InvalidError{Problems: []change.Problem{{Field: "branch", Message: "is not a valid branch name"}}}
	}
	if _, err := m.Head(ctx, branch); err == nil {
		return ImportProposal{}, fmt.Errorf("%w: %s", gitops.ErrBranchExists, branch)
	}
	msg := req.Message
	if req.RecheckMessage != "" && v.Plan.Recheck() {
		msg = req.RecheckMessage
	}
	sha, err := m.CommitFiles(ctx, v.SHA, v.Plan.Writes(), msg)
	if err != nil {
		return ImportProposal{}, fmt.Errorf("commit the import: %w", err)
	}
	if err := e.pushProposal(ctx, repo, m, branch, sha); err != nil {
		return ImportProposal{}, err
	}
	e.log.Info("proposed import", "repo", repo.ID, "branch", branch, "commit", sha, "files", len(v.Plan.Writes()))
	return ImportProposal{ImportView: v, Branch: branch, Commit: sha, Message: msg}, nil
}

// yamlFiles reads the YAML files of a commit in one git process, reporting
// those over the size limit.
func yamlFiles(ctx context.Context, m *gitops.Mirror, commit string) (map[string][]byte, []importer.Problem, error) {
	tree, err := m.ListTree(ctx, commit)
	if err != nil {
		return nil, nil, err
	}
	var big []importer.Problem
	var paths, objects []string
	for _, f := range tree {
		if ext := path.Ext(f.Path); ext != ".yml" && ext != ".yaml" {
			continue
		}
		if f.Size > MaxImportFile {
			big = append(big, importer.Problem{SourceFile: f.Path, Message: "larger than 1 MiB; not read"})
			continue
		}
		paths, objects = append(paths, f.Path), append(objects, f.Object)
	}
	blobs, err := m.ReadBlobs(ctx, objects)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	for i, p := range paths {
		files[p] = blobs[i]
	}
	return files, big, nil
}

// importRepos resolves repository resources to registered repositories,
// reading each repository and branch once.
func (e *Engine) importRepos(ctx context.Context, repo change.Repo) func(importer.RepoRef) (importer.Tree, error) {
	type result struct {
		tree importer.Tree
		err  error
	}
	seen := map[string]result{}
	var all []change.Repo
	listed := false
	return func(ref importer.RepoRef) (importer.Tree, error) {
		if !listed {
			list, err := e.store.ListRepos(ctx)
			if err != nil {
				return importer.Tree{}, err
			}
			all, listed = list, true
		}
		other, err := matchRepo(repo, all, ref)
		if err != nil {
			return importer.Tree{}, err
		}
		key := other.ID + "\x00" + ref.Ref
		if r, ok := seen[key]; ok {
			return r.tree, r.err
		}
		t, err := e.readRepo(ctx, other, ref.Ref)
		seen[key] = result{t, err}
		return t, err
	}
}

func (e *Engine) readRepo(ctx context.Context, repo change.Repo, ref string) (importer.Tree, error) {
	m, err := e.fetch(ctx, repo)
	if err != nil {
		return importer.Tree{}, err
	}
	heads, err := m.ListHeads(ctx)
	if err != nil {
		return importer.Tree{}, err
	}
	branch, err := resourceBranch(ref, heads)
	if err != nil {
		return importer.Tree{}, err
	}
	head, ok := heads[branch]
	if !ok {
		return importer.Tree{}, fmt.Errorf("branch %s not found", branch)
	}
	files, _, err := yamlFiles(ctx, m, head)
	if err != nil {
		return importer.Tree{}, err
	}
	return importer.Tree{Files: files, Label: repoLabel(repo) + " at " + short(head)}, nil
}

var azureRepoURL = regexp.MustCompile(`^https://(?:dev\.azure\.com/([^/]+)|([A-Za-z0-9-]+)\.visualstudio\.com)/([^/]+)/_git/([^/]+)$`)

// azureParts returns the organisation, project and name of an Azure Repos
// URL, decoded and lowercased.
func azureParts(u string) (org, project, name string, ok bool) {
	m := azureRepoURL.FindStringSubmatch(u)
	if m == nil {
		return "", "", "", false
	}
	dec := func(s string) string {
		if d, err := url.PathUnescape(s); err == nil {
			s = d
		}
		return strings.ToLower(s)
	}
	return dec(m[1] + m[2]), dec(m[3]), dec(m[4]), true
}

// matchRepo finds the registered repository a resource names (WU-047):
// Azure Repos (type git) by project and name in the importing repository's
// organisation, the project defaulting to its own; GitHub by owner/name.
func matchRepo(importing change.Repo, all []change.Repo, ref importer.RepoRef) (change.Repo, error) {
	missing := fmt.Errorf("%s is not registered in Plumbix; register it to import its templates", ref.Name)
	switch ref.Kind {
	case "git", "":
		org, project, _, ok := azureParts(importing.URL)
		if !ok {
			return change.Repo{}, errors.New("type git names an Azure Repos repository, and this repository is not on Azure DevOps")
		}
		wantProject, wantName := project, strings.ToLower(ref.Name)
		if p, n, found := strings.Cut(ref.Name, "/"); found {
			wantProject, wantName = strings.ToLower(p), strings.ToLower(n)
		}
		for _, r := range all {
			if o, p, n, ok := azureParts(r.URL); ok && o == org && p == wantProject && n == wantName {
				return r, nil
			}
		}
		return change.Repo{}, missing
	case "github":
		for _, r := range all {
			name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(r.URL, "https://github.com/"), "/"), ".git")
			if r.Host == change.HostGitHub && strings.EqualFold(name, ref.Name) {
				return r, nil
			}
		}
		return change.Repo{}, missing
	}
	return change.Repo{}, fmt.Errorf("repository type %s is not supported", ref.Kind)
}

// resourceBranch is the branch a resource's ref names: refs/heads/x or x,
// and with no ref main, else master.
func resourceBranch(ref string, heads map[string]string) (string, error) {
	switch {
	case ref == "":
		for _, b := range []string{"main", "master"} {
			if _, ok := heads[b]; ok {
				return b, nil
			}
		}
		return "", errors.New("it has no main or master branch; give the resource a ref")
	case strings.HasPrefix(ref, "refs/heads/"):
		return strings.TrimPrefix(ref, "refs/heads/"), nil
	case strings.HasPrefix(ref, "refs/tags/"):
		return "", fmt.Errorf("ref %s: tags are not supported yet", ref)
	case strings.HasPrefix(ref, "refs/"):
		return "", fmt.Errorf("ref %s is not a branch", ref)
	}
	return ref, nil
}

// repoLabel names a repository after the last part of its URL.
func repoLabel(r change.Repo) string {
	name := path.Base(strings.TrimSuffix(r.URL, ".git"))
	if d, err := url.PathUnescape(name); err == nil {
		name = d
	}
	return name
}
