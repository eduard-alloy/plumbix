package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
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
	go func() {
		done <- runServe(ctx, []string{"--listen", "127.0.0.1:0", "--data-dir", t.TempDir()}, io.Discard, logs)
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
