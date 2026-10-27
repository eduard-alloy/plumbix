package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/gitops"
	"github.com/eduard-alloy/plumbix/internal/importer"
	"github.com/eduard-alloy/plumbix/internal/importer/azure"
)

// TestImportPreview (WU-046): the Azure pipelines at a branch head are
// converted; other files are ignored, huge ones reported; nothing is written.
func TestImportPreview(t *testing.T) {
	h := newHarness(t, Config{})
	head := h.repo.Commit("main", map[string]string{
		"pipelines/azure-pipelines-ci.yml": "trigger: [main]\ncontainer: node:20\nsteps:\n  - script: npm test\n",
		"config/settings.yml":              "name: x\n",
		"pipelines/huge.yml":               "trigger: none\nsteps:\n  - script: x\n# " + strings.Repeat("x", 1<<20) + "\n",
	}, "azure pipelines")
	v, err := h.eng.ImportPreview(ctx, h.repoID, "main", azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if v.SHA != head || v.Branch != "main" || len(v.Result.Pipelines) != 1 {
		t.Fatalf("view = %+v", v)
	}
	p := v.Result.Pipelines[0]
	if p.Name != "ci" || p.File != ".plumbix/pipelines/ci.yaml" || p.SourceFile != "pipelines/azure-pipelines-ci.yml" || !strings.Contains(string(p.Content), "npm test") {
		t.Errorf("pipeline = %+v", p)
	}
	if len(v.Result.Problems) != 1 || v.Result.Problems[0].SourceFile != "pipelines/huge.yml" {
		t.Errorf("problems = %+v", v.Result.Problems)
	}
	if files, _ := h.eng.TriggeredPipelines(ctx, h.repoID, "main"); len(files.Pipelines) != 0 {
		t.Error("the preview wrote pipelines")
	}
}

// TestMatchRepo (WU-047): a repository resource resolves only to a
// registered repository: Azure Repos by project and name in the importing
// repository's organisation, GitHub by owner and name.
func TestMatchRepo(t *testing.T) {
	app := change.Repo{ID: "app", Host: change.HostAzureDevOps, URL: "https://dev.azure.com/acme/Shop/_git/app"}
	all := []change.Repo{app,
		{ID: "infra", Host: change.HostAzureDevOps, URL: "https://dev.azure.com/acme/Shop/_git/infra"},
		{ID: "lib", Host: change.HostAzureDevOps, URL: "https://acme.visualstudio.com/Shop/_git/My%20Lib"},
		{ID: "elsewhere", Host: change.HostAzureDevOps, URL: "https://dev.azure.com/other/Shop/_git/infra2"},
		{ID: "tools", Host: change.HostGitHub, URL: "https://github.com/someone/tools"},
		{ID: "slash", Host: change.HostGitHub, URL: "https://github.com/someone/kit.git/"},
	}
	for _, c := range []struct {
		kind, name, want string
	}{
		{"git", "Shop/infra", "infra"},
		{"git", "infra", "infra"},
		{"git", "shop/INFRA", "infra"},
		{"git", "Shop/My Lib", "lib"},
		{"github", "someone/Tools", "tools"},
		{"github", "someone/kit", "slash"},
		{"git", "Other/infra", "not registered"},
		{"git", "infra2", "not registered"},
		{"github", "someone/x", "not registered"},
		{"bitbucket", "a/b", "bitbucket"},
	} {
		got, err := matchRepo(app, all, importer.RepoRef{Kind: c.kind, Name: c.name})
		if err != nil {
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s %s: err %v, want %q", c.kind, c.name, err, c.want)
			}
			continue
		}
		if got.ID != c.want {
			t.Errorf("%s %s = %s, want %s", c.kind, c.name, got.ID, c.want)
		}
	}
}

// TestResourceBranch (WU-047): refs/heads/x and x name a branch; no ref
// means main, else master; tags are not supported yet.
func TestResourceBranch(t *testing.T) {
	heads := map[string]string{"master": "a", "release/1": "b"}
	for ref, want := range map[string]string{"": "master", "refs/heads/release/1": "release/1", "release/1": "release/1", "refs/tags/v1": "tags", "refs/pull/1": "refs/pull/1"} {
		got, err := resourceBranch(ref, heads)
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, want) {
			t.Errorf("%q = %q, want %q", ref, got, want)
		}
	}
	if _, err := resourceBranch("", map[string]string{"dev": "a"}); err == nil {
		t.Error("no main or master should be an error")
	}
}

// TestImportPreviewTemplates (WU-047): the preview inlines a template of
// the repository; a resource no registered repository matches disables
// its reference with a note saying so.
func TestImportPreviewTemplates(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("main", map[string]string{
		"azure-pipelines.yml": `trigger: [main]
resources:
  repositories:
    - repository: infra
      type: github
      name: someone/infra
stages:
  - template: ci/build.yml
  - template: deploy.yml@infra
`,
		"ci/build.yml": "stages:\n  - stage: build\n    jobs:\n      - job: test\n        container: node:20\n        steps:\n          - script: npm test\n",
	}, "pipeline with templates")
	v, err := h.eng.ImportPreview(ctx, h.repoID, "main", azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Result.Pipelines) != 1 {
		t.Fatalf("result = %+v", v.Result)
	}
	steps := v.Result.Pipelines[0].Steps
	if len(steps) != 2 || steps[0].Name != "build-test" || steps[1].Name != "deploy" || steps[0].Reason != "" || steps[0].File != "ci/build.yml" {
		t.Errorf("steps = %+v", steps)
	}
	if steps[1].Reason != "template" || !strings.Contains(steps[1].Note, "not registered") {
		t.Errorf("resource step = %+v", steps[1])
	}
}

// TestImportPropose (WU-048): the plan is committed on a new branch in one
// commit, the change it opens needs a person, and once the files are on the
// target a re-check finds nothing to do.
func TestImportPropose(t *testing.T) {
	h := newHarness(t, Config{})
	head := h.repo.Commit("main", map[string]string{
		"azure-pipelines.yml":    "trigger: [main]\ncontainer: node:20\nsteps:\n  - script: npm test\n",
		"pipelines/deploy.yml":   "trigger: none\npool: {vmImage: ubuntu-latest}\nsteps:\n  - script: ./deploy.sh\n",
		"docs/not-a-pipeline.md": "x\n",
	}, "azure pipelines")
	v, err := h.eng.ImportPreview(ctx, h.repoID, "main", azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Plan.Files) != 2 || v.Plan.Files[0].Status != importer.StatusAdded {
		t.Fatalf("preview plan = %+v", v.Plan)
	}
	p, err := h.eng.ImportPropose(ctx, h.repoID, ImportRequest{TargetBranch: "main", Message: "Import Azure pipelines"}, azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Branch != "plumbix/import-"+short(head) || p.SHA != head || p.Commit == "" {
		t.Fatalf("proposal = %+v", p)
	}
	if files := strings.Fields(h.remote("diff", "--name-only", head, p.Commit)); strings.Join(files, ",") != ".plumbix/pipelines/azure-pipelines.yaml,.plumbix/pipelines/deploy.yaml" {
		t.Fatalf("committed = %v", files)
	}
	if _, err := h.eng.ImportPropose(ctx, h.repoID, ImportRequest{TargetBranch: "main", Message: "again"}, azure.Source{}); !errors.Is(err, gitops.ErrBranchExists) {
		t.Errorf("same branch twice: %v", err)
	}
	c := h.submit(p.Branch)
	if r := h.wait(h.validate(c.ID).ID); r.Outcome != change.NeedsHuman {
		t.Errorf("outcome = %s, want needs_human", r.Outcome)
	}

	written := map[string]string{}
	for _, f := range v.Plan.Files {
		written[f.Path] = string(f.Content)
	}
	h.repo.Commit("main", written, "the import lands")
	if _, err := h.eng.ImportPropose(ctx, h.repoID, ImportRequest{TargetBranch: "main", Branch: "plumbix/again", Message: "again"}, azure.Source{}); !errors.Is(err, ErrUpToDate) {
		t.Errorf("re-check with nothing new: %v", err)
	}
	h.repo.Commit("main", map[string]string{"azure-pipelines.yml": "trigger: [main]\ncontainer: node:22\nsteps:\n  - script: npm test\n"}, "newer node")
	p, err = h.eng.ImportPropose(ctx, h.repoID, ImportRequest{TargetBranch: "main", Branch: "plumbix/recheck", Message: "Import", RecheckMessage: "Re-check"}, azure.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if msg := h.remote("log", "-1", "--format=%s", p.Commit); msg != "Re-check" {
		t.Errorf("re-check commit message = %q", msg)
	}
	got := h.remote("show", p.Commit+":.plumbix/pipelines/azure-pipelines.yaml")
	if !strings.Contains(got, "node:22") || len(p.Plan.Files) != 2 || p.Plan.Files[0].Status != importer.StatusUpdated || p.Plan.Files[1].Status != importer.StatusUnchanged {
		t.Errorf("re-check = %+v\n%s", p.Plan, got)
	}
}
