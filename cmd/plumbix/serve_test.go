package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/auth"
)

// syncBuffer is a goroutine-safe writer for capturing server logs.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func clearPlumbixEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PLUMBIX_CONFIG", "PLUMBIX_DATA_DIR", "PLUMBIX_LISTEN"} {
		t.Setenv(k, "")
	}
}

func TestServeRefusesNonLoopback(t *testing.T) {
	clearPlumbixEnv(t)
	for _, addr := range []string{"0.0.0.0:6400", ":6400", "192.168.1.10:6400"} {
		var stdout, stderr bytes.Buffer
		code := runServe(context.Background(), []string{"--listen", addr, "--data-dir", t.TempDir()}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("%s: exit code = %d, want 1", addr, code)
		}
		if !strings.Contains(stderr.String(), "loopback") {
			t.Errorf("%s: stderr = %q, want explanation of the loopback rule", addr, stderr.String())
		}
	}
}

func TestServeStartsAndStops(t *testing.T) {
	clearPlumbixEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan int, 1)
	dataDir := filepath.Join(t.TempDir(), "data")
	go func() {
		done <- runServe(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, io.Discard, logs)
	}()

	addr := waitForListenAddr(t, logs)
	resp, err := http.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if body["version"] != version {
		t.Errorf("version = %q, want %q", body["version"], version)
	}

	token, err := auth.ReadToken(dataDir)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	check := "http://" + addr + "/api/v1/pipelines/check"
	pipelineYAML := "version: 1\nsteps:\n  - {name: a, image: x, run: x}\n"
	if code := post(t, check, "", pipelineYAML); code != http.StatusUnauthorized {
		t.Errorf("check without token: status %d, want 401", code)
	}
	if code := post(t, check, token, pipelineYAML); code != http.StatusOK {
		t.Errorf("check with token: status %d, want 200", code)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; logs:\n%s", code, logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

func TestServeRefusesLooseTokenFile(t *testing.T) {
	clearPlumbixEnv(t)
	dataDir := t.TempDir()
	if _, err := auth.EnsureToken(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dataDir, auth.TokenFile), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := runServe(context.Background(), []string{"--listen", "127.0.0.1:0", "--data-dir", dataDir}, io.Discard, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "chmod 600") {
		t.Fatalf("exit %d, stderr %q; want refusal explaining chmod 600", code, stderr.String())
	}
}

// post sends body to url with an optional bearer token and returns the status.
func post(t *testing.T, url, token, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// waitForListenAddr reads the JSON logs until the "server listening" entry appears.
func waitForListenAddr(t *testing.T, logs *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sc := bufio.NewScanner(strings.NewReader(logs.String()))
		for sc.Scan() {
			var entry map[string]any
			if json.Unmarshal(sc.Bytes(), &entry) == nil && entry["msg"] == "server listening" {
				if addr, ok := entry["addr"].(string); ok {
					return addr
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never logged its address; logs:\n%s", logs.String())
	return ""
}
