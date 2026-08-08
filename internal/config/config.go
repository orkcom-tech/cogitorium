// Package config loads server configuration with precedence:
// flags > environment (COGITORIUM_*) > config file > defaults.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Listen is the HTTP listen address, e.g. "127.0.0.1:8688".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database and everything the server owns on disk.
	DataDir string `yaml:"data_dir"`
	// LogLevel is one of debug|info|warn|error.
	LogLevel string `yaml:"log_level"`
}

func Defaults() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home dir (e.g. bare container) — fall back to CWD-relative data.
		home = "."
	}
	return Config{
		Listen:   "127.0.0.1:8688",
		DataDir:  filepath.Join(home, ".cogitorium"),
		LogLevel: "info",
	}
}

// Load builds the effective config. path is the --config flag value; empty
// means: use $COGITORIUM_CONFIG if set, else <data-dir>/config.yaml if it
// exists, else the file layer is skipped. dataDirOverride is the --data flag
// value (empty if not given) so the default config probe honors the
// effective data dir, not just the built-in default.
func Load(path, dataDirOverride string) (Config, error) {
	cfg := Defaults()

	probeDir := cfg.DataDir
	if v := os.Getenv("COGITORIUM_DATA_DIR"); v != "" {
		probeDir = v
	}
	if dataDirOverride != "" {
		probeDir = dataDirOverride
	}

	explicit := path != ""
	if !explicit {
		if env := os.Getenv("COGITORIUM_CONFIG"); env != "" {
			path, explicit = env, true
		} else {
			path = filepath.Join(probeDir, "config.yaml")
		}
	}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		slog.Info("config file loaded", "path", path)
	case os.IsNotExist(err) && !explicit:
		slog.Debug("no config file, using defaults", "path", path)
	default:
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}

	if v := os.Getenv("COGITORIUM_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("COGITORIUM_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("COGITORIUM_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	return cfg, nil
}

// SlogLevel maps LogLevel to a slog.Level, defaulting to info on garbage.
func (c Config) SlogLevel() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		slog.Warn("unknown log_level, using info", "log_level", c.LogLevel)
		return slog.LevelInfo
	}
}
