package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
)

// Own images (WU-033, A32): an operator's image defined by a Dockerfile
// written on the Images page, stored under <dir>/own/<name>/ and built on
// the server. The directory is the build context.

// Labels an own image carries when this server built it.
const (
	LabelOwn        = "org.plumbix.own"
	LabelDockerfile = "org.plumbix.dockerfile" // sha256 of the Dockerfile it was built from
	// OwnTag is what a pipeline's bare `image: <name>` resolves to.
	OwnTag = "latest"
	// MaxDockerfileBytes bounds a Dockerfile written on the page.
	MaxDockerfileBytes = 64 << 10
)

var ownNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// Own is one operator-defined image.
type Own struct {
	Name       string    `json:"name"`
	Dockerfile string    `json:"dockerfile"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// OwnRef is the reference pipelines use for an own image.
func OwnRef(name string) string { return name + ":" + OwnTag }

func (s *Service) ownDir(name string) string { return filepath.Join(s.dir, "own", name) }

// ListOwn returns the own images by name.
func (s *Service) ListOwn() ([]Own, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "own"))
	if errors.Is(err, os.ErrNotExist) {
		return []Own{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Own{}
	for _, e := range entries {
		if !e.IsDir() || !ownNameRe.MatchString(e.Name()) {
			continue
		}
		o, err := s.GetOwn(e.Name())
		if err != nil {
			if errors.Is(err, change.ErrNotFound) {
				continue // a directory without a Dockerfile is not an image
			}
			return nil, err
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetOwn returns one own image, or change.ErrNotFound.
func (s *Service) GetOwn(name string) (Own, error) {
	if !ownNameRe.MatchString(name) {
		return Own{}, change.ErrNotFound
	}
	path := filepath.Join(s.ownDir(name), "Dockerfile")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Own{}, change.ErrNotFound
	}
	if err != nil {
		return Own{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Own{}, err
	}
	return Own{Name: name, Dockerfile: string(data), UpdatedAt: info.ModTime().UTC()}, nil
}

// PutOwn validates and stores an own image's Dockerfile, creating or
// replacing it.
func (s *Service) PutOwn(name, dockerfile string) (Own, error) {
	var problems []change.Problem
	add := func(field, msg string) { problems = append(problems, change.Problem{Field: field, Message: msg}) }
	if !ownNameRe.MatchString(name) {
		add("name", "must be lowercase letters, digits, dots, dashes and underscores, starting with a letter or digit, at most 63 characters, no slash")
	} else if _, shipped := IsShipped(name); shipped {
		add("name", fmt.Sprintf("%s is an image Plumbix ships; choose another name", name))
	}
	trimmed := strings.TrimSpace(dockerfile)
	switch {
	case trimmed == "":
		add("dockerfile", "is empty")
	case len(dockerfile) > MaxDockerfileBytes:
		add("dockerfile", fmt.Sprintf("is %d bytes; the limit is %d", len(dockerfile), MaxDockerfileBytes))
	case !hasFrom(trimmed):
		add("dockerfile", "has no FROM instruction")
	}
	if len(problems) > 0 {
		return Own{}, &change.InvalidError{Problems: problems}
	}
	dir := s.ownDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Own{}, err
	}
	if !strings.HasSuffix(dockerfile, "\n") {
		dockerfile += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return Own{}, err
	}
	return s.GetOwn(name)
}

// DeleteOwn removes an own image's definition. A local image already built
// from it stays until Docker prunes it. change.ErrNotFound when unknown.
func (s *Service) DeleteOwn(name string) error {
	if _, err := s.GetOwn(name); err != nil {
		return err
	}
	return os.RemoveAll(s.ownDir(name))
}

// hasFrom reports whether a Dockerfile has a FROM instruction outside
// comments; the instruction and its image may be separated by any blank.
func hasFrom(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "FROM") {
			return true
		}
	}
	return false
}

// dockerfileHash identifies the Dockerfile an image was built from.
func dockerfileHash(dockerfile string) string {
	sum := sha256.Sum256([]byte(dockerfile))
	return hex.EncodeToString(sum[:])
}

// ownContextTar packs an own image's directory as its build context and
// returns the Dockerfile exactly as packed, so the build's label hashes the
// file the image is built from and nothing else.
func (s *Service) ownContextTar(name string) (*bytesReader, string, error) {
	fsys := os.DirFS(s.ownDir(name))
	data, err := fs.ReadFile(fsys, "Dockerfile")
	if err != nil {
		return nil, "", fmt.Errorf("the Dockerfile of %s is gone: %w", name, err)
	}
	// The context is packed from the bytes just read, not from a second
	// read of the directory, so the two cannot differ.
	tarball, err := ContextTar(staticFS{"Dockerfile": data})
	return tarball, string(data), err
}

// staticFS is an in-memory fs.FS of the files a context holds.
type staticFS map[string][]byte

func (f staticFS) Open(name string) (fs.File, error) {
	if name == "." {
		return &staticDir{entries: f}, nil
	}
	data, ok := f[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &staticFile{name: name, Reader: bytes.NewReader(data), size: int64(len(data))}, nil
}

type staticDir struct {
	entries staticFS
	read    bool
}

func (d *staticDir) Stat() (fs.FileInfo, error) { return staticInfo{name: ".", dir: true}, nil }
func (d *staticDir) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (d *staticDir) Close() error               { return nil }
func (d *staticDir) ReadDir(int) ([]fs.DirEntry, error) {
	if d.read {
		return nil, nil
	}
	d.read = true
	names := make([]string, 0, len(d.entries))
	for n := range d.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, fs.FileInfoToDirEntry(staticInfo{name: n, size: int64(len(d.entries[n]))}))
	}
	return out, nil
}

type staticFile struct {
	name string
	*bytes.Reader
	size int64
}

func (f *staticFile) Stat() (fs.FileInfo, error) { return staticInfo{name: f.name, size: f.size}, nil }
func (f *staticFile) Close() error               { return nil }

type staticInfo struct {
	name string
	size int64
	dir  bool
}

func (i staticInfo) Name() string { return i.name }
func (i staticInfo) Size() int64  { return i.size }
func (i staticInfo) Mode() fs.FileMode {
	return map[bool]fs.FileMode{true: fs.ModeDir | 0o755, false: 0o644}[i.dir]
}
func (i staticInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (i staticInfo) IsDir() bool        { return i.dir }
func (i staticInfo) Sys() any           { return nil }

// canonical gives a reference its implicit tag, so my-validator and
// my-validator:latest are one image.
func canonical(ref string) string {
	last := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		last = ref[i+1:]
	}
	if strings.Contains(last, ":") || strings.Contains(last, "@") {
		return ref
	}
	return ref + ":" + OwnTag
}
