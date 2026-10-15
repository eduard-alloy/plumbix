// Package secrets loads Plumbix secrets from a dotenv file that only the
// owner can read. Errors never include secret values or file content.
package secrets

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads KEY=VALUE lines from path. Blank lines and lines starting with
// # are ignored, an "export " prefix is allowed, and values may be wrapped
// in single or double quotes. Keys with empty values are left out. The file
// must not be readable or writable by group or others.
func Load(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("secrets file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("secrets file %s has permissions %04o; other users could read it. Run: chmod 600 %s", path, perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	out := map[string]string{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !keyRe.MatchString(key) {
			return nil, fmt.Errorf("secrets file %s line %d: expected KEY=VALUE", path, n)
		}
		if seen[key] {
			return nil, fmt.Errorf("secrets file %s line %d: %s is set more than once", path, n, key)
		}
		seen[key] = true
		value, err := unquote(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("secrets file %s line %d (%s): %v", path, n, key, err)
		}
		if value != "" {
			out[key] = value
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	return out, nil
}

func unquote(v string) (string, error) {
	if v == "" || (v[0] != '"' && v[0] != '\'') {
		return v, nil
	}
	if len(v) < 2 || v[len(v)-1] != v[0] {
		return "", fmt.Errorf("unterminated quote")
	}
	return v[1 : len(v)-1], nil
}
