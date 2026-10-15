package config

import (
	"strings"
	"testing"
)

func TestValidateListen(t *testing.T) {
	accepted := []string{
		"127.0.0.1:6400",
		"127.0.0.2:6400",
		"[::1]:6400",
		"localhost:6400",
		"LocalHost:6400",
		"127.0.0.1:0",
	}
	for _, addr := range accepted {
		if err := ValidateListen(addr); err != nil {
			t.Errorf("ValidateListen(%q) = %v, want nil", addr, err)
		}
	}

	rejected := []string{
		":6400",
		"0.0.0.0:6400",
		"[::]:6400",
		"192.168.1.10:6400",
		"10.0.0.1:6400",
		"example.com:6400",
		"127.0.0.1",
		"",
		"localhost.evil.com:6400",
	}
	for _, addr := range rejected {
		err := ValidateListen(addr)
		if err == nil {
			t.Errorf("ValidateListen(%q) = nil, want error", addr)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("ValidateListen(%q) error %q should explain the loopback rule", addr, err)
		}
	}
}
