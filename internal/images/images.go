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
	"sort"
	"strings"
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

// ShippedImages are the validators Plumbix ships, built from validators/.
var ShippedImages = []Shipped{
	{Name: "plumbix-reference-validator", Dockerfile: "validators/reference/Dockerfile", Purpose: "The deterministic validator: checks each acceptance criterion against step results and declared paths; must approve before any merge (A12)."},
	{Name: "plumbix-ai-validator", Dockerfile: "validators/ai/Dockerfile", Purpose: "The AI validator run inside the pipeline: asks its reviewer profile's model (Anthropic or any OpenAI-compatible endpoint) and writes a verdict."},
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
	Ref       string    `json:"ref"` // name:tag as the pipeline writes it
	Present   bool      `json:"present"`
	Created   time.Time `json:"created,omitempty"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
	Digest    string    `json:"digest,omitempty"`
	Shipped   bool      `json:"shipped"`
	Purpose   string    `json:"purpose,omitempty"`
	Version   string    `json:"version,omitempty"` // the org.plumbix.version label, shipped images
	Status    string    `json:"status"`
	UsedBy    []Use     `json:"used_by"`
}

// Service inspects and builds images.
type Service struct {
	docker  Docker
	version string // the running server's version, compared with the label
	builds  *builds
}

// New returns a Service over docker, writing build logs under dir.
func New(docker Docker, version, dir string) *Service {
	return &Service{docker: docker, version: version, builds: newBuilds(dir)}
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

// List describes every image in uses plus the shipped ones, sorted with
// shipped images first and then by reference.
func (s *Service) List(ctx context.Context, uses map[string][]Use) ([]Info, error) {
	refs := map[string]bool{}
	for ref := range uses {
		refs[ref] = true
	}
	for _, sh := range ShippedImages {
		refs[ShippedRef(sh.Name)] = true
	}
	var out []Info
	for ref := range refs {
		info, err := s.inspect(ctx, ref)
		if err != nil {
			return nil, err
		}
		info.UsedBy = uses[ref]
		if info.UsedBy == nil {
			info.UsedBy = []Use{}
		}
		sort.Slice(info.UsedBy, func(i, j int) bool {
			a, b := info.UsedBy[i], info.UsedBy[j]
			return a.Repo < b.Repo || (a.Repo == b.Repo && a.Step < b.Step)
		})
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shipped != out[j].Shipped {
			return out[i].Shipped
		}
		return out[i].Ref < out[j].Ref
	})
	return out, nil
}

// inspect asks Docker about one reference.
func (s *Service) inspect(ctx context.Context, ref string) (Info, error) {
	info := Info{Ref: ref, Status: StatusPresent}
	if sh, ok := IsShipped(ref); ok {
		info.Shipped, info.Purpose = true, sh.Purpose
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
	if info.Shipped {
		labels := map[string]string{}
		if res.Config != nil {
			labels = res.Config.Labels
		}
		info.Version = labels[LabelVersion]
		switch {
		case info.Version == "":
			info.Status = StatusUnlabelled
		case info.Version == s.version:
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

// nameOf strips the tag from a reference.
func nameOf(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > 0 && !strings.Contains(ref[i:], "/") {
		return ref[:i]
	}
	return ref
}
