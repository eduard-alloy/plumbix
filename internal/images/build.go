package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	plumbix "github.com/eduard-alloy/plumbix"
	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/moby/moby/client"
)

// Build statuses.
const (
	BuildRunning   = "running"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
)

// Build is one image build, as the page follows it.
type Build struct {
	ID         string     `json:"id"`
	Image      string     `json:"image"` // the reference built
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Log        string     `json:"log"`
}

// builds keeps the builds of this server's lifetime, one running at a time,
// with their logs on disk.
type builds struct {
	dir     string
	mu      sync.Mutex
	byID    map[string]*Build
	running bool
}

func newBuilds(dir string) *builds { return &builds{dir: dir, byID: map[string]*Build{}} }

// bytesReader is the build context type ContextTar returns.
type bytesReader = bytes.Reader

// buildable describes one build: where the context comes from, the
// Dockerfile path inside it, the tag and the labels.
type buildable struct {
	ref, dockerfile, source string
	labels                  map[string]string
	context                 func() (*bytes.Reader, error)
}

// resolve finds what name means: a shipped image or an own one.
func (s *Service) resolve(name string) (buildable, error) {
	if sh, ok := IsShipped(name); ok {
		return buildable{ref: ShippedRef(sh.Name), dockerfile: sh.Dockerfile, source: sh.Dockerfile,
			labels:  map[string]string{LabelVersion: s.version, LabelShipped: sh.Name},
			context: func() (*bytes.Reader, error) { return ContextTar(plumbix.BuildContext) }}, nil
	}
	own, err := s.GetOwn(strings.TrimSuffix(name, ":"+OwnTag))
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return buildable{}, fmt.Errorf("%w: %s", ErrNotShipped, name)
		}
		return buildable{}, err
	}
	return buildable{ref: OwnRef(own.Name), dockerfile: "Dockerfile", source: "the Dockerfile written on the Images page",
		labels:  map[string]string{LabelVersion: s.version, LabelOwn: own.Name, LabelDockerfile: dockerfileHash(own.Dockerfile)},
		context: func() (*bytes.Reader, error) { return s.ownContextTar(own.Name) }}, nil
}

// Build starts building a shipped or own image and returns at once; the
// build runs in the background and its log is read with GetBuild. One
// build at a time: a second request while one runs is ErrBusy.
func (s *Service) Build(ctx context.Context, name string) (Build, error) {
	what, err := s.resolve(name)
	if err != nil {
		return Build{}, err
	}
	ref := what.ref
	b := s.builds
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return Build{}, ErrBusy
	}
	id, err := randomID()
	if err != nil {
		b.mu.Unlock()
		return Build{}, err
	}
	rec := &Build{ID: id, Image: ref, Status: BuildRunning, StartedAt: time.Now().UTC()}
	b.byID[id] = rec
	b.running = true
	b.mu.Unlock()
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		b.finish(rec, err)
		return *rec, err
	}
	// The build outlives the request: it runs under its own context and
	// reports through the record.
	go func() {
		err := s.runBuild(context.Background(), what, b.logPath(id))
		b.finish(rec, err)
	}()
	return b.snapshot(rec), nil
}

func (b *builds) logPath(id string) string { return filepath.Join(b.dir, id+".log") }

func (b *builds) finish(rec *Build, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().UTC()
	rec.FinishedAt = &now
	rec.Status = BuildSucceeded
	if err != nil {
		rec.Status, rec.Error = BuildFailed, err.Error()
	}
	b.running = false
}

// snapshot copies a record with its log read from disk.
func (b *builds) snapshot(rec *Build) Build {
	out := *rec
	if data, err := os.ReadFile(b.logPath(rec.ID)); err == nil {
		out.Log = string(data)
	}
	return out
}

// GetBuild returns a build by id with its log so far.
func (s *Service) GetBuild(id string) (Build, bool) {
	b := s.builds
	b.mu.Lock()
	rec, ok := b.byID[id]
	b.mu.Unlock()
	if !ok {
		return Build{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshot(rec), true
}

// runBuild sends the embedded context to Docker and streams the build
// output into the log file. The Docker API answers with a JSON message
// stream; stream text goes to the log as it arrives and an error message
// ends the build.
func (s *Service) runBuild(ctx context.Context, what buildable, logPath string) (err error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	ref := what.ref
	fmt.Fprintf(logFile, "plumbix: building %s from %s (server %s)\n", ref, what.source, s.version)
	tarball, err := what.context()
	if err != nil {
		fmt.Fprintf(logFile, "plumbix: %v\n", err)
		return err
	}
	res, err := s.docker.ImageBuild(ctx, tarball, client.ImageBuildOptions{
		Tags:       []string{ref},
		Dockerfile: what.dockerfile,
		Labels:     what.labels,
		Remove:     true,
	})
	if err != nil {
		fmt.Fprintf(logFile, "plumbix: build request failed: %v\n", err)
		return fmt.Errorf("build request failed: %w", err)
	}
	defer res.Body.Close()
	if err := copyBuildStream(res.Body, logFile); err != nil {
		fmt.Fprintf(logFile, "plumbix: build failed: %v\n", err)
		return err
	}
	fmt.Fprintf(logFile, "plumbix: built %s\n", ref)
	return nil
}

// buildMessage is one line of the Docker build stream.
type buildMessage struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

// copyBuildStream writes the stream's text to w and returns the build's
// error, if the daemon reported one.
func copyBuildStream(r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(r)
	for {
		var m buildMessage
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read build output: %w", err)
		}
		if m.Stream != "" {
			io.WriteString(w, m.Stream)
		} else if m.Status != "" {
			io.WriteString(w, m.Status+"\n")
		}
		if m.Error != "" {
			msg := m.ErrorDetail.Message
			if msg == "" {
				msg = m.Error
			}
			return fmt.Errorf("%s", msg)
		}
	}
}

// ContextTar packs an embedded file system into a tar build context.
func ContextTar(fsys fs.FS) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "." {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("pack build context: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

func randomID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ib_" + hex.EncodeToString(b), nil
}
