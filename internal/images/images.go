// Package images is what the server knows about the container images its
// pipelines run (WU-032, A32): which ones exist locally, how old they are,
// who uses them, and for the two images Plumbix ships, whether the local
// build matches the server's version and how to rebuild it from the
// sources embedded in the binary.
package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Labels a shipped image carries when this server built it.
const (
	LabelVersion = "org.plumbix.version"
	LabelShipped = "org.plumbix.shipped"
	// Tag is the tag shipped images are built with; pipelines name
	// plumbix-ai-validator:dev.
	Tag = "dev"
)

// Shipped describes one of the images Plumbix ships from its own sources.
type Shipped struct {
	Name       string `json:"name"`
	Dockerfile string `json:"dockerfile"` // path inside the build context
	Purpose    string `json:"purpose"`
}

// ShippedImages are the three validators Plumbix ships, built from validators/.
var ShippedImages = []Shipped{
	{Name: "plumbix-reference-validator", Dockerfile: "validators/reference/Dockerfile", Purpose: "The deterministic validator: checks each acceptance criterion against step results and declared paths; must approve before any merge (A12)."},
	{Name: "plumbix-ai-validator", Dockerfile: "validators/ai/Dockerfile", Purpose: "The AI validator run inside the pipeline: asks its reviewer profile's model (Anthropic or any OpenAI-compatible endpoint) and writes a verdict."},
	{Name: "plumbix-claude-code-validator", Dockerfile: "validators/claude-code/Dockerfile", Purpose: "Claude Code run inside the pipeline (A33): reviews with tools in the checked-out code on the token of a claude-code-image reviewer profile, minted with claude setup-token or an API key."},
}

// Docker is what the service needs from the Docker client.
type Docker interface {
	ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImageBuild(ctx context.Context, buildContext io.Reader, options client.ImageBuildOptions) (client.ImageBuildResult, error)
}

// Use is one pipeline step that runs an image.
type Use struct {
	RepoID    string `json:"repo_id"`
	Repo      string `json:"repo"` // display name
	Step      string `json:"step"`
	Validator bool   `json:"validator"`
	// Pipeline names the triggered pipeline the step is in (WU-045); empty
	// for the change pipeline.
	Pipeline string `json:"pipeline,omitempty"`
}

// Status of a shipped image's local build against the running server.
const (
	StatusMissing    = "missing"    // no local image
	StatusCurrent    = "current"    // built by this server version
	StatusStale      = "stale"      // built by another version
	StatusUnlabelled = "unlabelled" // present but not built through Plumbix (docker build by hand)
	StatusPresent    = "present"    // a third-party image that exists locally
)

// Info is one image as the page shows it.
type Info struct {
	Ref        string    `json:"ref"` // name:tag as the pipeline writes it
	Present    bool      `json:"present"`
	Created    time.Time `json:"created,omitempty"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	Shipped    bool      `json:"shipped"`
	Own        bool      `json:"own"`                  // defined by a Dockerfile on the Images page (WU-033)
	Dockerfile string    `json:"dockerfile,omitempty"` // the own image's Dockerfile
	Purpose    string    `json:"purpose,omitempty"`
	Version    string    `json:"version,omitempty"` // the org.plumbix.version label, shipped images
	Status     string    `json:"status"`
	UsedBy     []Use     `json:"used_by"`
}

// Service inspects and builds images.
type Service struct {
	docker  Docker
	version string // the running server's version, compared with the label
	dir     string // <data_dir>/images: builds/ for logs, own/ for own images
	builds  *builds
	// beforePack, when set by a test, runs just before an own image's
	// context is packed, to interleave a save with a build.
	beforePack func()
}

// New returns a Service over docker, keeping build logs and own images under dir.
func New(docker Docker, version, dir string) *Service {
	return &Service{docker: docker, version: version, dir: dir, builds: newBuilds(filepath.Join(dir, "builds"))}
}

// Version is the server version shipped images are compared with.
func (s *Service) Version() string { return s.version }

// ShippedRef is the reference pipelines use for a shipped image.
func ShippedRef(name string) string { return name + ":" + Tag }

// IsShipped reports whether ref names a shipped image, and which.
func IsShipped(ref string) (Shipped, bool) {
	for _, s := range ShippedImages {
		if ref == ShippedRef(s.Name) || ref == s.Name {
			return s, true
		}
	}
	return Shipped{}, false
}

// List describes every image in uses plus the shipped and own ones, sorted
// with shipped images first, own images next, then by reference.
func (s *Service) List(ctx context.Context, uses map[string][]Use) ([]Info, error) {
	byRef := map[string][]Use{}
	for ref, u := range uses {
		c := canonical(ref)
		byRef[c] = append(byRef[c], u...)
	}
	refs := map[string]bool{}
	for ref := range byRef {
		refs[ref] = true
	}
	for _, sh := range ShippedImages {
		refs[ShippedRef(sh.Name)] = true
	}
	owns, err := s.ListOwn()
	if err != nil {
		return nil, err
	}
	ownByRef := map[string]Own{}
	for _, o := range owns {
		refs[OwnRef(o.Name)] = true
		ownByRef[OwnRef(o.Name)] = o
	}
	var out []Info
	for ref := range refs {
		info, err := s.inspect(ctx, ref, ownByRef)
		if err != nil {
			return nil, err
		}
		info.UsedBy = byRef[ref]
		if info.UsedBy == nil {
			info.UsedBy = []Use{}
		}
		sort.Slice(info.UsedBy, func(i, j int) bool {
			a, b := info.UsedBy[i], info.UsedBy[j]
			return a.Repo < b.Repo || (a.Repo == b.Repo && a.Step < b.Step)
		})
		out = append(out, info)
	}
	rank := func(in Info) int {
		switch {
		case in.Shipped:
			return 0
		case in.Own:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return out[i].Ref < out[j].Ref
	})
	return out, nil
}

// inspect asks Docker about one reference.
func (s *Service) inspect(ctx context.Context, ref string, owns map[string]Own) (Info, error) {
	info := Info{Ref: ref, Status: StatusPresent}
	if sh, ok := IsShipped(ref); ok {
		info.Shipped, info.Purpose = true, sh.Purpose
	}
	own, isOwn := owns[ref]
	if isOwn {
		info.Own, info.Dockerfile = true, own.Dockerfile
	}
	res, err := s.docker.ImageInspect(ctx, ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			info.Status = StatusMissing
			return info, nil
		}
		return Info{}, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	info.Present = true
	info.SizeBytes = res.Size
	info.Digest = res.ID
	if t, err := time.Parse(time.RFC3339Nano, res.Created); err == nil {
		info.Created = t
	}
	labels := map[string]string{}
	if res.Config != nil {
		labels = res.Config.Labels
	}
	switch {
	case info.Shipped:
		info.Version = labels[LabelVersion]
		switch {
		case info.Version == "":
			info.Status = StatusUnlabelled
		case info.Version == s.version:
			info.Status = StatusCurrent
		default:
			info.Status = StatusStale
		}
	case isOwn:
		// Current means built from the Dockerfile as it is stored now.
		info.Version = labels[LabelVersion]
		switch built := labels[LabelDockerfile]; {
		case built == "":
			info.Status = StatusUnlabelled
		case built == dockerfileHash(own.Dockerfile):
			info.Status = StatusCurrent
		default:
			info.Status = StatusStale
		}
	}
	return info, nil
}

// ErrNotShipped is returned when a build is asked for an image Plumbix does
// not ship (own images arrive with WU-033).
var ErrNotShipped = errors.New("not an image Plumbix ships")

// ErrBusy is returned when a build is already running.
var ErrBusy = errors.New("a build is already running")
