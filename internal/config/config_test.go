package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaults(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load(Overrides{}, envFrom(nil), home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != DefaultListen {
		t.Errorf("listen = %q, want %q", cfg.Server.Listen, DefaultListen)
	}
	if want := filepath.Join(home, ".plumbix"); cfg.DataDir != want {
		t.Errorf("data dir = %q, want %q", cfg.DataDir, want)
	}
}

func TestConfigFileInDataDir(t *testing.T) {
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "server:\n  listen: 127.0.0.1:7000\n")
	cfg, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:7000" {
		t.Errorf("listen = %q, want value from file", cfg.Server.Listen)
	}
}

func TestEnvBeatsFile(t *testing.T) {
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "server:\n  listen: 127.0.0.1:7000\n")
	env := envFrom(map[string]string{"PLUMBIX_DATA_DIR": dataDir, "PLUMBIX_LISTEN": "127.0.0.1:7001"})
	cfg, err := Load(Overrides{}, env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:7001" {
		t.Errorf("listen = %q, want env value", cfg.Server.Listen)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("data dir = %q, want env value", cfg.DataDir)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	env := envFrom(map[string]string{"PLUMBIX_LISTEN": "127.0.0.1:7001"})
	cfg, err := Load(Overrides{Listen: "127.0.0.1:7002", DataDir: t.TempDir()}, env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:7002" {
		t.Errorf("listen = %q, want flag value", cfg.Server.Listen)
	}
}

func TestExplicitConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	writeFile(t, path, "server:\n  listen: 127.0.0.1:7003\n")
	env := envFrom(map[string]string{"PLUMBIX_CONFIG": path})
	cfg, err := Load(Overrides{DataDir: t.TempDir()}, env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:7003" {
		t.Errorf("listen = %q, want value from explicit file", cfg.Server.Listen)
	}
}

func TestMissingExplicitConfigIsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := Load(Overrides{ConfigPath: missing, DataDir: t.TempDir()}, envFrom(nil), t.TempDir()); err == nil {
		t.Fatal("expected error for missing explicit config file")
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "server:\n  listne: 127.0.0.1:7000\n")
	_, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "listne") {
		t.Fatalf("err = %v, want error naming the unknown key", err)
	}
}

func TestEmptyFileKeepsDefaults(t *testing.T) {
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "")
	cfg, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != DefaultListen {
		t.Errorf("listen = %q, want default", cfg.Server.Listen)
	}
}
