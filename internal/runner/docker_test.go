//go:build integration

package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const testImage = "alpine:3.20"

// lockedBuffer collects logs written from the runner's goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newDocker(t *testing.T) *Docker {
	t.Helper()
	d, err := NewDocker()
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() {
		left, err := d.managedContainers(context.Background(), jobName(t))
		if err != nil {
			t.Errorf("list containers: %v", err)
		}
		if len(left) > 0 {
			t.Errorf("containers left behind: %v", left)
		}
		_ = d.Close()
	})
	return d
}

// jobName gives each test's containers a unique job label.
func jobName(t *testing.T) string {
	return strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
}

func job(t *testing.T, script string, log *lockedBuffer) Job {
	return Job{
		Name:      jobName(t),
		Image:     testImage,
		Script:    script,
		Workspace: t.TempDir(),
		Timeout:   time.Minute,
		CPUs:      1,
		MemoryMB:  128,
		Log:       log,
	}
}

func TestOutputAndExitCode(t *testing.T) {
	d := newDocker(t)
	log := &lockedBuffer{}
	res, err := d.Run(context.Background(), job(t, "echo to-stdout; echo to-stderr >&2; exit 3", log))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.TimedOut {
		t.Errorf("result = %+v, want exit 3", res)
	}
	if !strings.Contains(log.String(), "to-stdout") || !strings.Contains(log.String(), "to-stderr") {
		t.Errorf("log = %q, want both streams", log.String())
	}
}

func TestWorkspaceIsMountedAndWritable(t *testing.T) {
	d := newDocker(t)
	log := &lockedBuffer{}
	j := job(t, "pwd; echo hello > result.txt", log)
	if res, err := d.Run(context.Background(), j); err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v log=%s", res, err, log.String())
	}
	if !strings.Contains(log.String(), WorkspacePath) {
		t.Errorf("working directory log = %q, want %s", log.String(), WorkspacePath)
	}
	got, err := os.ReadFile(filepath.Join(j.Workspace, "result.txt"))
	if err != nil || strings.TrimSpace(string(got)) != "hello" {
		t.Fatalf("host file = %q, %v", got, err)
	}
}

func TestEnvReachesContainer(t *testing.T) {
	d := newDocker(t)
	log := &lockedBuffer{}
	j := job(t, `echo "mode=$MODE"`, log)
	j.Env = map[string]string{"MODE": "ci value"}
	if _, err := d.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "mode=ci value") {
		t.Errorf("log = %q", log.String())
	}
}

func TestEmptyScriptUsesEntrypoint(t *testing.T) {
	d := newDocker(t)
	res, err := d.Run(context.Background(), job(t, "", &lockedBuffer{}))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestTimeoutKillsContainer(t *testing.T) {
	d := newDocker(t)
	j := job(t, "sleep 60", &lockedBuffer{})
	j.Timeout = 2 * time.Second
	start := time.Now()
	res, err := d.Run(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Errorf("result = %+v, want timed out", res)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %s, want close to the 2s timeout", elapsed)
	}
}

func TestCancelKillsContainer(t *testing.T) {
	d := newDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(2 * time.Second); cancel() }()
	_, err := d.Run(ctx, job(t, "sleep 60", &lockedBuffer{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestMissingImageIsPulled(t *testing.T) {
	d := newDocker(t)
	const img = "busybox:1.37.0"
	d.removeImage(context.Background(), img)
	j := job(t, "true", &lockedBuffer{})
	j.Image = img
	res, err := d.Run(context.Background(), j)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestUnpullableImageIsError(t *testing.T) {
	d := newDocker(t)
	j := job(t, "true", &lockedBuffer{})
	j.Image = "plumbix.invalid/does-not-exist:never"
	if _, err := d.Run(context.Background(), j); err == nil {
		t.Fatal("expected an infrastructure error for an image that cannot be pulled")
	}
}

// removeImage deletes an image if present, so a test can force a pull.
func (d *Docker) removeImage(ctx context.Context, ref string) {
	_, _ = d.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{})
}
