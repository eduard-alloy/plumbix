package config

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ValidateListen checks that addr is host:port with a loopback host.
// Plumbix has no authentication yet, so it must never listen on an address
// other machines can reach. An empty host means all interfaces and is rejected.
func ValidateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q must be host:port on a loopback address: %w", addr, err)
	}
	if isLoopback(host) {
		return nil
	}
	return fmt.Errorf("listen address %q is not a loopback address: Plumbix has no authentication yet, so it only listens on 127.0.0.1, ::1 or localhost", addr)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
