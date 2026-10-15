//go:build integration

package runner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/api"
	"github.com/eduard-alloy/plumbix/internal/auth"
)

// TestStepCannotUseHostAPI checks ARCHITECTURE risk 1 end to end. On Docker
// Desktop a step container can reach the host's loopback (WU-003), so the
// API's protection is the token (WU-013). The container probes a real API
// server through every route Docker offers and must never get data back:
// only 401 or no connection.
func TestStepCannotUseHostAPI(t *testing.T) {
	token, err := auth.EnsureToken(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.NewHandler("dev", token)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	script := fmt.Sprintf(`
gw=$(ip route | awk '/default/ {print $3}')
for h in host.docker.internal gateway.docker.internal "$gw" 172.17.0.1 192.168.65.254; do
  [ -n "$h" ] || continue
  out=$(wget -q -T 2 -O- --post-data='version: 1' "http://$h:%d/api/v1/pipelines/check" 2>&1)
  echo "route $h: ${out:-no response}"
done
`, port)

	d := newDocker(t)
	log := &lockedBuffer{}
	if _, err := d.Run(context.Background(), job(t, script, log)); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	t.Logf("probe results:\n%s", out)
	if strings.Contains(out, `"valid"`) {
		t.Fatalf("a step container got a real API response:\n%s", out)
	}
	if strings.Contains(out, "401") {
		t.Log("the API port is reachable from containers here (Docker Desktop); the token rejected every call")
	}
}
