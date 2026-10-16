package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestRunnerDefaults(t *testing.T) {
	cfg, err := Load(Overrides{DataDir: t.TempDir()}, envFrom(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Runner
	if r.MaxParallel != runtime.NumCPU() || r.DefaultTimeout != 30*time.Minute || r.StepCPUs != 2 || r.StepMemoryMB != 4096 {
		t.Errorf("runner defaults = %+v", r)
	}
}

func TestRunnerFromFile(t *testing.T) {
	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "runner:\n  max_parallel: 3\n  default_timeout: 5m\n  step_cpus: 0.5\n  step_memory_mb: 512\n")
	cfg, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := Runner{MaxParallel: 3, DefaultTimeout: 5 * time.Minute, StepCPUs: 0.5, StepMemoryMB: 512}
	if cfg.Runner != want {
		t.Errorf("runner = %+v, want %+v", cfg.Runner, want)
	}
}

func TestRunnerValidation(t *testing.T) {
	bad := []string{
		"runner:\n  max_parallel: 0\n",
		"runner:\n  default_timeout: 0s\n",
		"runner:\n  step_cpus: -1\n",
		"runner:\n  step_memory_mb: 3\n",
	}
	for _, content := range bad {
		dataDir := t.TempDir()
		writeFile(t, filepath.Join(dataDir, "config.yaml"), content)
		if _, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir()); err == nil {
			t.Errorf("config %q: expected validation error", content)
		}
	}
}

func TestSecretsFileAndFixRounds(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretsFile != filepath.Join(dataDir, ".env") || cfg.FixRounds.Max != DefaultFixRounds {
		t.Errorf("defaults: secrets %q fix rounds %d", cfg.SecretsFile, cfg.FixRounds.Max)
	}
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "secrets_file: /repo/.env\nfix_rounds:\n  max: 5\n")
	cfg, err = Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil || cfg.SecretsFile != "/repo/.env" || cfg.FixRounds.Max != 5 {
		t.Errorf("from file: %+v, %v", cfg, err)
	}
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "fix_rounds:\n  max: 0\n")
	if _, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir()); err == nil {
		t.Error("fix_rounds.max 0 accepted")
	}
}

func TestPollerConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil || !cfg.Poller.Enabled || cfg.Poller.Interval != DefaultPollInterval {
		t.Fatalf("defaults: %+v, %v", cfg.Poller, err)
	}
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "poller:\n  enabled: false\n  interval: 2m\n")
	cfg, err = Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir())
	if err != nil || cfg.Poller.Enabled || cfg.Poller.Interval != 2*time.Minute {
		t.Errorf("from file: %+v, %v", cfg.Poller, err)
	}
	writeFile(t, filepath.Join(dataDir, "config.yaml"), "poller:\n  interval: 1s\n")
	if _, err := Load(Overrides{DataDir: dataDir}, envFrom(nil), t.TempDir()); err == nil {
		t.Error("1s interval accepted")
	}
}
