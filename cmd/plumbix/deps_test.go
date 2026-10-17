package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestServerBinaryLinksNoModelSDK keeps the core free of AI (Q07, A26): the
// server builds review briefs and checks verdicts, but the model client
// lives only in the validator images.
func TestServerBinaryLinksNoModelSDK(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Split(string(out), "\n") {
		if strings.Contains(dep, "anthropic-sdk-go") || strings.Contains(dep, "plumbix/internal/aivalidator") {
			t.Errorf("cmd/plumbix links %s; the model client belongs in validators/ only", dep)
		}
	}
}
