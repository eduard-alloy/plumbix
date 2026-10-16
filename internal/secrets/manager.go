package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager owns a secrets file: it holds the current values and rewrites the
// file when one changes. Only the owner can read the file (0600). Values
// never appear in errors.
type Manager struct {
	path     string
	mu       sync.RWMutex
	values   map[string]string
	loaded   time.Time // mtime of the file the values came from
	watchers []func()
}

// Open loads path, or starts empty when it does not exist yet. A file that
// others can read is refused, as in Load.
func Open(path string) (*Manager, error) {
	m := &Manager{path: path, values: map[string]string{}}
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

// reload reads the file when it changed since the last read. Callers hold
// m.mu for writing. A missing file means no secrets.
func (m *Manager) reload() error {
	info, err := os.Stat(m.path)
	if errors.Is(err, os.ErrNotExist) {
		m.values, m.loaded = map[string]string{}, time.Time{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	if info.ModTime().Equal(m.loaded) {
		return nil
	}
	values, err := Load(m.path)
	if err != nil {
		return err
	}
	m.values, m.loaded = values, info.ModTime()
	return nil
}

// refresh picks up a file edited by hand while the server runs. Errors
// keep the last good values.
func (m *Manager) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.reload()
}

// Path is the file the manager writes.
func (m *Manager) Path() string { return m.path }

// Values returns a copy of every secret.
func (m *Manager) Values() map[string]string {
	m.refresh()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.values))
	for k, v := range m.values {
		out[k] = v
	}
	return out
}

// Get returns one secret.
func (m *Manager) Get(key string) (string, bool) {
	m.refresh()
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.values[key]
	return v, ok
}

// Keys returns the set keys, sorted.
func (m *Manager) Keys() []string {
	m.refresh()
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.values))
	for k := range m.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Watch registers fn to run after every change.
func (m *Manager) Watch(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchers = append(m.watchers, fn)
}

// Set stores value under key and rewrites the file. An empty value is not
// allowed: use Unset.
func (m *Manager) Set(key, value string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("secret key %q: must match %s", key, keyRe)
	}
	if value == "" {
		return fmt.Errorf("secret %s: a value is required; remove the key instead", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("secret %s: the value must be a single line", key)
	}
	quoted, err := quote(value)
	if err != nil {
		return fmt.Errorf("secret %s: %v", key, err)
	}
	m.mu.Lock()
	_ = m.reload()
	if err := m.rewrite(key, key+"="+quoted); err != nil {
		m.mu.Unlock()
		return err
	}
	m.values[key] = value
	m.mu.Unlock()
	m.notify()
	return nil
}

// Unset removes key from the file. A key that is not set is left alone.
func (m *Manager) Unset(key string) error {
	m.mu.Lock()
	_ = m.reload()
	if _, ok := m.values[key]; !ok {
		m.mu.Unlock()
		return nil
	}
	if err := m.rewrite(key, ""); err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.values, key)
	m.mu.Unlock()
	m.notify()
	return nil
}

func (m *Manager) notify() {
	m.mu.RLock()
	watchers := append([]func(){}, m.watchers...)
	m.mu.RUnlock()
	for _, fn := range watchers {
		fn()
	}
}

// rewrite replaces key's line with line (or removes it when line is empty),
// appending a new key at the end, and writes the file atomically with mode
// 0600. Every other line, comments included, stays as it is. Callers hold
// m.mu.
func (m *Manager) rewrite(key, line string) error {
	data, err := os.ReadFile(m.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read secrets file: %w", err)
	}
	var out []string
	replaced := false
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if len(data) == 0 {
			break
		}
		if lineKey(l) == key {
			if !replaced && line != "" {
				out = append(out, line)
			}
			replaced = true
			continue
		}
		out = append(out, l)
	}
	if !replaced && line != "" {
		out = append(out, line)
	}
	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write secrets file: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("replace secrets file: %w", err)
	}
	if info, err := os.Stat(m.path); err == nil {
		m.loaded = info.ModTime()
	}
	return nil
}

// lineKey returns the key a KEY=VALUE line sets, or "" for other lines.
func lineKey(l string) string {
	l = strings.TrimSpace(l)
	if l == "" || strings.HasPrefix(l, "#") {
		return ""
	}
	l = strings.TrimPrefix(l, "export ")
	key, _, ok := strings.Cut(l, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

// quote wraps a value so Load reads it back unchanged.
func quote(v string) (string, error) {
	if !strings.ContainsAny(v, " \t#'\"") {
		return v, nil
	}
	switch {
	case !strings.Contains(v, "'"):
		return "'" + v + "'", nil
	case !strings.Contains(v, `"`):
		return `"` + v + `"`, nil
	}
	return "", errors.New("a value cannot contain both kinds of quote")
}
