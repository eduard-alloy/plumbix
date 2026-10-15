// Package auth protects the Plumbix API with a local bearer token stored in
// a file only the machine's owner can read.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TokenFile is the token's file name inside the data directory.
const TokenFile = "api-token"

const tokenBytes = 32

var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EnsureToken returns the API token, creating the data directory (0700) and
// the token file (0600) on first use. It refuses a token file that others
// can read or write, or whose content is malformed.
func EnsureToken(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, TokenFile)
	tok, err := ReadToken(dataDir)
	if !errors.Is(err, os.ErrNotExist) {
		return tok, err
	}

	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	tok = hex.EncodeToString(buf)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ReadToken(dataDir) // another process created it first
	}
	if err != nil {
		return "", fmt.Errorf("create API token file: %w", err)
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("write API token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write API token file: %w", err)
	}
	return tok, nil
}

// ReadToken reads and checks the API token. Clients such as the CLI use it.
// A missing file returns an error wrapping os.ErrNotExist.
func ReadToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, TokenFile)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("API token file %s does not exist; start the server with plumbix serve to create it: %w", path, err)
	}
	if err != nil {
		return "", fmt.Errorf("check API token file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("API token file %s has permissions %04o; other users could read it. Run: chmod 600 %s", path, perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if !tokenRe.MatchString(tok) {
		return "", fmt.Errorf("API token file %s is malformed; delete it and restart plumbix serve to generate a new token", path)
	}
	return tok, nil
}
