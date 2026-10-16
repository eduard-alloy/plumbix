// Package config loads Plumbix server settings from defaults, an optional
// YAML file, environment variables and command-line flags, in increasing
// order of precedence.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultListen is the address the server listens on when nothing else is set.
const DefaultListen = "127.0.0.1:6400"

// DefaultShutdownTimeout is how long in-flight requests get to finish on shutdown.
const DefaultShutdownTimeout = 10 * time.Second

// Environment variable names.
const (
	EnvConfig  = "PLUMBIX_CONFIG"
	EnvDataDir = "PLUMBIX_DATA_DIR"
	EnvListen  = "PLUMBIX_LISTEN"
)

// Runner defaults.
const (
	DefaultStepTimeout  = 30 * time.Minute
	DefaultStepCPUs     = 2.0
	DefaultStepMemoryMB = 4096
	minStepMemoryMB     = 6 // Docker refuses memory limits below 6 MiB.
)

// DefaultFixRounds is how many fix rounds a change gets before escalation.
const DefaultFixRounds = 3

// Config holds the resolved server settings.
type Config struct {
	DataDir     string    `yaml:"data_dir"`
	SecretsFile string    `yaml:"secrets_file"` // default <data_dir>/.env
	Server      Server    `yaml:"server"`
	Runner      Runner    `yaml:"runner"`
	FixRounds   FixRounds `yaml:"fix_rounds"`
	Poller      Poller    `yaml:"poller"`
}

// Poller configures push detection.
type Poller struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// Poller defaults.
const (
	DefaultPollInterval = 30 * time.Second
	MinPollInterval     = 5 * time.Second
)

// FixRounds limits the fix loop.
type FixRounds struct {
	Max int `yaml:"max"`
}

// Runner holds pipeline execution settings.
type Runner struct {
	MaxParallel    int           `yaml:"max_parallel"`
	DefaultTimeout time.Duration `yaml:"default_timeout"`
	StepCPUs       float64       `yaml:"step_cpus"`
	StepMemoryMB   int           `yaml:"step_memory_mb"`
}

// Server holds HTTP server settings.
type Server struct {
	Listen string `yaml:"listen"`
}

// Overrides carries values given as command-line flags. Empty fields are unset.
type Overrides struct {
	ConfigPath string
	DataDir    string
	Listen     string
}

// Load resolves the configuration. getenv reads environment variables and
// homeDir is used to build the default data directory.
func Load(flags Overrides, getenv func(string) string, homeDir string) (Config, error) {
	cfg := Config{
		DataDir:   filepath.Join(homeDir, ".plumbix"),
		Server:    Server{Listen: DefaultListen},
		FixRounds: FixRounds{Max: DefaultFixRounds},
		Poller:    Poller{Enabled: true, Interval: DefaultPollInterval},
		Runner: Runner{
			MaxParallel:    runtime.NumCPU(),
			DefaultTimeout: DefaultStepTimeout,
			StepCPUs:       DefaultStepCPUs,
			StepMemoryMB:   DefaultStepMemoryMB,
		},
	}

	dataDir := first(flags.DataDir, getenv(EnvDataDir), cfg.DataDir)
	path, explicit := flags.ConfigPath, true
	if path == "" {
		path = getenv(EnvConfig)
	}
	if path == "" {
		path, explicit = filepath.Join(dataDir, "config.yaml"), false
	}

	if err := applyFile(&cfg, path, explicit); err != nil {
		return Config{}, err
	}

	cfg.DataDir = first(flags.DataDir, getenv(EnvDataDir), cfg.DataDir)
	cfg.Server.Listen = first(flags.Listen, getenv(EnvListen), cfg.Server.Listen)
	if cfg.SecretsFile == "" {
		cfg.SecretsFile = filepath.Join(cfg.DataDir, ".env")
	}
	if err := cfg.Runner.validate(); err != nil {
		return Config{}, err
	}
	if cfg.Poller.Interval < MinPollInterval {
		return Config{}, fmt.Errorf("poller.interval must be at least %s, got %s", MinPollInterval, cfg.Poller.Interval)
	}
	if cfg.FixRounds.Max < 1 {
		return Config{}, fmt.Errorf("fix_rounds.max must be at least 1, got %d", cfg.FixRounds.Max)
	}
	return cfg, nil
}

func (r Runner) validate() error {
	switch {
	case r.MaxParallel < 1:
		return fmt.Errorf("runner.max_parallel must be at least 1, got %d", r.MaxParallel)
	case r.DefaultTimeout <= 0:
		return fmt.Errorf("runner.default_timeout must be above zero, got %s", r.DefaultTimeout)
	case r.StepCPUs <= 0:
		return fmt.Errorf("runner.step_cpus must be above zero, got %g", r.StepCPUs)
	case r.StepMemoryMB < minStepMemoryMB:
		return fmt.Errorf("runner.step_memory_mb must be at least %d, got %d", minStepMemoryMB, r.StepMemoryMB)
	}
	return nil
}

// applyFile decodes the YAML file at path into cfg. A missing file is an
// error only when the path was given explicitly.
func applyFile(cfg *Config, path string, explicit bool) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	return nil
}

// first returns the first non-empty value.
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
