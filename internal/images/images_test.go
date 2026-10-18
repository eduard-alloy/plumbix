package images

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/eduard-alloy/plumbix/internal/change"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/client"
)

// fakeDocker answers inspects from a table and records builds.
type fakeDocker struct {
	mu      sync.Mutex
	images  map[string]client.ImageInspectResult
	builds  []client.ImageBuildOptions
	entries []string // tar entries of the last build context
	stream  string   // what the daemon answers
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.images[ref]; ok {
		return r, nil
	}
	return client.ImageInspectResult{}, cerrdefs.ErrNotFound
}

func (f *fakeDocker) ImageBuild(_ context.Context, ctxTar io.Reader, opts client.ImageBuildOptions) (client.ImageBuildResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, opts)
	f.entries = nil
	tr := tar.NewReader(ctxTar)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		f.entries = append(f.entries, h.Name)
	}
	return client.ImageBuildResult{Body: io.NopCloser(strings.NewReader(f.stream))}, nil
}

func inspected(created string, size int64, labels map[string]string) client.ImageInspectResult {
	var r client.ImageInspectResult
	r.ID = "sha256:" + strings.Repeat("ab", 32)
	r.Created = created
	r.Size = size
	r.Config = &dockerspec.DockerOCIImageConfig{}
	r.Config.Labels = labels
	return r
}

func TestListMergesPipelineUsesAndShippedImagesWithStatus(t *testing.T) {
	f := &fakeDocker{images: map[string]client.ImageInspectResult{
		"golang:1.27":                     inspected("2026-10-01T10:00:00Z", 800_000_000, nil),
		"plumbix-ai-validator:dev":        inspected("2026-10-18T18:42:58Z", 20_000_000, map[string]string{LabelVersion: "3ce0061", LabelShipped: "plumbix-ai-validator"}),
		"plumbix-reference-validator:dev": inspected("2026-10-16T21:20:49Z", 12_000_000, nil),
	}}
	s := New(f, "3ce0061", t.TempDir())
	uses := map[string][]Use{
		"golang:1.27":              {{RepoID: "r_1", Repo: "plumbix", Step: "test"}, {RepoID: "r_1", Repo: "plumbix", Step: "build"}},
		"plumbix-ai-validator:dev": {{RepoID: "r_1", Repo: "plumbix", Step: "ai-grok", Validator: true}},
		"node:24-alpine":           {{RepoID: "r_1", Repo: "plumbix", Step: "web"}},
	}
	list, err := s.List(context.Background(), uses)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]string, len(list))
	for i, in := range list {
		refs[i] = in.Ref
	}
	want := []string{"plumbix-ai-validator:dev", "plumbix-claude-code-validator:dev", "plumbix-reference-validator:dev", "golang:1.27", "node:24-alpine"}
	if strings.Join(refs, " ") != strings.Join(want, " ") {
		t.Fatalf("refs = %v, want shipped first then by name %v", refs, want)
	}
	ai, cc, ref, golang, node := list[0], list[1], list[2], list[3], list[4]
	if !ai.Shipped || ai.Status != StatusCurrent || ai.Version != "3ce0061" || !ai.Present || ai.SizeBytes != 20_000_000 || len(ai.UsedBy) != 1 || !ai.UsedBy[0].Validator || ai.Purpose == "" {
		t.Errorf("ai = %+v", ai)
	}
	if !cc.Shipped || cc.Status != StatusMissing || cc.Present || len(cc.UsedBy) != 0 || cc.Purpose == "" {
		t.Errorf("claude code validator never built = %+v", cc)
	}
	if !ref.Shipped || ref.Status != StatusUnlabelled || len(ref.UsedBy) != 0 {
		t.Errorf("reference validator built by hand = %+v", ref)
	}
	if golang.Shipped || golang.Status != StatusPresent || golang.Created.Year() != 2026 || len(golang.UsedBy) != 2 || golang.UsedBy[0].Step != "build" {
		t.Errorf("golang = %+v", golang)
	}
	if node.Present || node.Status != StatusMissing || node.Digest != "" {
		t.Errorf("missing image = %+v", node)
	}

	// Another server version makes the AI image stale.
	stale := New(f, "deadbee", t.TempDir())
	list, _ = stale.List(context.Background(), nil)
	if list[0].Status != StatusStale {
		t.Errorf("status under another version = %s", list[0].Status)
	}
}

func TestBuildSendsTheEmbeddedContextAndKeepsTheLog(t *testing.T) {
	f := &fakeDocker{images: map[string]client.ImageInspectResult{}, stream: `{"stream":"Step 1/9 : FROM golang:1.27-alpine AS build\n"}` + "\n" + `{"stream":" ---> abc\n"}` + "\n" + `{"aux":{"ID":"sha256:123"}}` + "\n"}
	s := New(f, "3ce0061", t.TempDir())
	if _, err := s.Build(context.Background(), "golang:1.27"); !errors.Is(err, ErrNotShipped) {
		t.Errorf("building a third-party image: %v", err)
	}
	b, err := s.Build(context.Background(), "plumbix-ai-validator")
	if err != nil {
		t.Fatal(err)
	}
	if b.Image != "plumbix-ai-validator:dev" || b.Status != BuildRunning || b.ID == "" {
		t.Errorf("build = %+v", b)
	}
	done := waitBuild(t, s, b.ID)
	if done.Status != BuildSucceeded || !strings.Contains(done.Log, "Step 1/9") || !strings.Contains(done.Log, "built plumbix-ai-validator:dev") {
		t.Errorf("finished build = %+v", done)
	}
	f.mu.Lock()
	opts, entries := f.builds[0], f.entries
	f.mu.Unlock()
	if len(opts.Tags) != 1 || opts.Tags[0] != "plumbix-ai-validator:dev" || opts.Dockerfile != "validators/ai/Dockerfile" || opts.Labels[LabelVersion] != "3ce0061" || opts.Labels[LabelShipped] != "plumbix-ai-validator" || !opts.Remove {
		t.Errorf("build options = %+v", opts)
	}
	has := func(name string) bool {
		for _, e := range entries {
			if e == name {
				return true
			}
		}
		return false
	}
	for _, need := range []string{"go.mod", "go.sum", "validators/ai/Dockerfile", "validators/ai/main.go", "internal/aivalidator/client.go", "internal/validation/verdict.go"} {
		if !has(need) {
			t.Errorf("build context lacks %s (%d entries)", need, len(entries))
		}
	}

	// A failing build: the daemon's error ends it as failed with the message.
	f.stream = `{"stream":"Step 1/9\n"}` + "\n" + `{"errorDetail":{"message":"The command '/bin/sh -c go build' returned a non-zero code: 1"},"error":"The command '/bin/sh -c go build' returned a non-zero code: 1"}` + "\n"
	b2, err := s.Build(context.Background(), "plumbix-reference-validator:dev")
	if err != nil {
		t.Fatal(err)
	}
	done = waitBuild(t, s, b2.ID)
	if done.Status != BuildFailed || !strings.Contains(done.Error, "non-zero code") || !strings.Contains(done.Log, "build failed") {
		t.Errorf("failed build = %+v", done)
	}
	if _, ok := s.GetBuild("ib_nope"); ok {
		t.Error("unknown build found")
	}
}

func TestOneBuildAtATime(t *testing.T) {
	f := &fakeDocker{images: map[string]client.ImageInspectResult{}, stream: ""}
	slow := &slowDocker{fakeDocker: f, release: make(chan struct{})}
	s := New(slow, "v", t.TempDir())
	first, err := s.Build(context.Background(), "plumbix-ai-validator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Build(context.Background(), "plumbix-reference-validator"); !errors.Is(err, ErrBusy) {
		t.Errorf("second build while one runs: %v", err)
	}
	close(slow.release)
	if done := waitBuild(t, s, first.ID); done.Status != BuildSucceeded {
		t.Errorf("first build = %+v", done)
	}
	if _, err := s.Build(context.Background(), "plumbix-reference-validator"); err != nil {
		t.Errorf("build after the first finished: %v", err)
	}
}

// slowDocker holds a build until released.
type slowDocker struct {
	*fakeDocker
	release chan struct{}
}

func (d *slowDocker) ImageBuild(ctx context.Context, c io.Reader, o client.ImageBuildOptions) (client.ImageBuildResult, error) {
	<-d.release
	return d.fakeDocker.ImageBuild(ctx, c, o)
}

func waitBuild(t *testing.T, s *Service, id string) Build {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, ok := s.GetBuild(id)
		if !ok {
			t.Fatalf("build %s vanished", id)
		}
		if b.Status != BuildRunning {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("build %s did not finish", id)
	return Build{}
}

// TestOwnImagesAreStoredBuiltFromTheirDirectoryAndReportedAgainstTheirDockerfile (WU-033).
func TestOwnImagesAreStoredBuiltFromTheirDirectoryAndReportedAgainstTheirDockerfile(t *testing.T) {
	f := &fakeDocker{images: map[string]client.ImageInspectResult{}, stream: `{"stream":"Step 1/2 : FROM alpine:3.20\n"}` + "\n"}
	s := New(f, "3ce0061", t.TempDir())
	if list, _ := s.ListOwn(); len(list) != 0 {
		t.Fatalf("own images on a fresh service = %+v", list)
	}
	// Validation names the field.
	for _, bad := range []struct{ name, file, field string }{
		{"My Image", "FROM alpine", "name"},
		{"plumbix-ai-validator", "FROM alpine", "name"},
		{"ok", "   ", "dockerfile"},
		{"ok", "RUN echo no base", "dockerfile"},
		{"ok", "FROM alpine\n" + strings.Repeat("# x\n", MaxDockerfileBytes/4), "dockerfile"},
	} {
		_, err := s.PutOwn(bad.name, bad.file)
		var inv *change.InvalidError
		if !errors.As(err, &inv) || inv.Problems[0].Field != bad.field {
			t.Errorf("PutOwn(%q, …) = %v, want a problem on %s", bad.name, err, bad.field)
		}
	}
	own, err := s.PutOwn("lint-tools", "FROM alpine:3.20\nRUN apk add --no-cache shellcheck")
	if err != nil {
		t.Fatal(err)
	}
	if own.Name != "lint-tools" || !strings.HasSuffix(own.Dockerfile, "shellcheck\n") || own.UpdatedAt.IsZero() {
		t.Errorf("stored = %+v", own)
	}
	if list, _ := s.ListOwn(); len(list) != 1 || list[0].Name != "lint-tools" {
		t.Errorf("own list = %+v", list)
	}

	// Not built yet: missing, own, with its Dockerfile; a bare reference in a
	// pipeline is the same image as name:latest.
	uses := map[string][]Use{"lint-tools": {{RepoID: "r_1", Repo: "plumbix", Step: "lint"}}}
	list, err := s.List(context.Background(), uses)
	if err != nil {
		t.Fatal(err)
	}
	var lt Info
	for _, in := range list {
		if in.Ref == "lint-tools:latest" {
			lt = in
		}
	}
	if !lt.Own || lt.Status != StatusMissing || lt.Dockerfile != own.Dockerfile || len(lt.UsedBy) != 1 || lt.Shipped {
		t.Errorf("own image before the build = %+v", lt)
	}
	n := len(ShippedImages) // own images follow the shipped ones
	if list[0].Shipped != true || list[n].Ref != "lint-tools:latest" {
		t.Errorf("order = %v", []string{list[0].Ref, list[1].Ref, list[n].Ref})
	}

	// Built from its own directory, labelled with the Dockerfile's hash.
	b, err := s.Build(context.Background(), "lint-tools")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitBuild(t, s, b.ID); done.Status != BuildSucceeded || done.Image != "lint-tools:latest" {
		t.Fatalf("build = %+v", done)
	}
	f.mu.Lock()
	opts, entries := f.builds[len(f.builds)-1], f.entries
	f.mu.Unlock()
	if opts.Tags[0] != "lint-tools:latest" || opts.Dockerfile != "Dockerfile" || opts.Labels[LabelOwn] != "lint-tools" || opts.Labels[LabelDockerfile] != dockerfileHash(own.Dockerfile) {
		t.Errorf("own build options = %+v", opts)
	}
	if len(entries) != 1 || entries[0] != "Dockerfile" {
		t.Errorf("own build context = %v, want just the Dockerfile", entries)
	}
	// Docker now has it with the labels: current. After an edit: stale.
	f.mu.Lock()
	f.images["lint-tools:latest"] = inspected("2026-10-18T19:00:00Z", 9_000_000, map[string]string{LabelVersion: "3ce0061", LabelOwn: "lint-tools", LabelDockerfile: dockerfileHash(own.Dockerfile)})
	f.mu.Unlock()
	list, _ = s.List(context.Background(), nil)
	if list[n].Status != StatusCurrent {
		t.Errorf("after the build = %s", list[n].Status)
	}
	if _, err := s.PutOwn("lint-tools", "FROM alpine:3.20\nRUN apk add --no-cache shellcheck shfmt"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(context.Background(), nil)
	if list[n].Status != StatusStale {
		t.Errorf("after editing the Dockerfile = %s", list[n].Status)
	}

	// Removal: the definition goes, the local image is Docker's to prune.
	if err := s.DeleteOwn("lint-tools"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOwn("lint-tools"); !errors.Is(err, change.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := s.Build(context.Background(), "lint-tools"); !errors.Is(err, ErrNotShipped) {
		t.Errorf("building a removed own image: %v", err)
	}
	if list, _ := s.List(context.Background(), nil); len(list) != len(ShippedImages) {
		t.Errorf("list after removal = %d images", len(list))
	}
}

// TestOwnBuildLabelsTheDockerfileItPacks (WU-033 note): a save between the
// build request and the packing of the context is what gets built and
// labelled; a FROM separated by a tab is a FROM.
func TestOwnBuildLabelsTheDockerfileItPacks(t *testing.T) {
	f := &fakeDocker{images: map[string]client.ImageInspectResult{}, stream: ""}
	s := New(f, "v", t.TempDir())
	if _, err := s.PutOwn("tabbed", "FROM\talpine:3.20\n"); err != nil {
		t.Fatalf("a FROM followed by a tab was refused: %v", err)
	}
	packing, saved := make(chan struct{}), make(chan struct{})
	s.beforePack = func() {
		close(packing)
		<-saved
	}
	first, err := s.Build(context.Background(), "tabbed")
	if err != nil {
		t.Fatal(err)
	}
	<-packing
	// Saved while the build is about to pack: the context and the label
	// follow the new file.
	edited := "FROM alpine:3.20\nRUN echo edited\n"
	if _, err := s.PutOwn("tabbed", edited); err != nil {
		t.Fatal(err)
	}
	close(saved)
	if done := waitBuild(t, s, first.ID); done.Status != BuildSucceeded {
		t.Fatalf("build = %+v", done)
	}
	f.mu.Lock()
	opts := f.builds[len(f.builds)-1]
	f.mu.Unlock()
	if opts.Labels[LabelDockerfile] != dockerfileHash(edited) {
		t.Errorf("label = %s, want the hash of the file as packed", opts.Labels[LabelDockerfile])
	}
}
