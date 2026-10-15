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

// Config holds the resolved server settings.
type Config struct {
	DataDir string `yaml:"data_dir"`
	Server  Server `yaml:"server"`
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
		DataDir: filepath.Join(homeDir, ".plumbix"),
		Server:  Server{Listen: DefaultListen},
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
	return cfg, nil
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
