// Package config loads server configuration with precedence:
// flags > environment (COGITORIUM_*) > config file > defaults.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Listen is the HTTP listen address, e.g. "127.0.0.1:8688".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database and everything the server owns on disk.
	DataDir string `yaml:"data_dir"`
	// LogLevel is one of debug|info|warn|error.
	LogLevel string `yaml:"log_level"`
	// ContextdPath is the contextd binary (Contextverse CLI) used for the
	// context layer. Default: "contextd" resolved from PATH.
	ContextdPath string `yaml:"contextd_path"`
	// Sandbox selects how gears and the terminal execute: "docker" isolates
	// them from the server's files, "subprocess" does not. Default "auto"
	// uses Docker when it answers and says so plainly when it does not.
	Sandbox string `yaml:"sandbox"`
	// SandboxImage is the container image gears run in.
	SandboxImage string `yaml:"sandbox_image"`
	// Terminal opens a shell in the UI. Off by default: it is interactive
	// code execution over HTTP, so switching it on is a deliberate act. It
	// also requires a sandbox — without one the request is refused rather
	// than served with the server's own file access.
	Terminal bool `yaml:"terminal"`

	// Egress is the master switch for agents reaching the internet. Off by
	// default, and deliberately reachable ONLY from this file and the
	// environment: there is no route, no setter and no database row, so no
	// agent and no tool call can flip it. Turning it on means editing a file
	// on the operator's own disk and restarting the server.
	//
	// It grants nothing on its own. An agent must additionally hold a grant
	// an operator drew on the blueprint, and every individual search still
	// stops the turn and waits for a person to approve that exact query.
	Egress bool `yaml:"egress"`

	// EgressKey is the credential for the search destination. It travels in a
	// header, never in a query string. Empty with Egress on is a startup
	// error, not a silent degradation to unauthenticated requests.
	EgressKey string `yaml:"egress_key"`

	// EgressApprovalBearer requires a real bearer token to grant the gate or
	// approve a search, refusing the implicit-admin that loopback otherwise
	// confers. Off by default because it would make the feature unusable on a
	// default single-operator install; the audit records which kind of
	// authentication each decision actually had, so a row is never mistaken
	// for stronger evidence than it is.
	EgressApprovalBearer bool `yaml:"egress_approval_bearer"`
}

func Defaults() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home dir (e.g. bare container) — fall back to CWD-relative data.
		home = "."
	}
	return Config{
		Listen:       "127.0.0.1:8688",
		DataDir:      filepath.Join(home, ".cogitorium"),
		LogLevel:     "info",
		ContextdPath: "contextd",
		Sandbox:      "auto",
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
	if v := os.Getenv("COGITORIUM_CONTEXTD"); v != "" {
		cfg.ContextdPath = v
	}
	if v := os.Getenv("COGITORIUM_SANDBOX"); v != "" {
		cfg.Sandbox = v
	}
	if v := os.Getenv("COGITORIUM_SANDBOX_IMAGE"); v != "" {
		cfg.SandboxImage = v
	}
	if v := os.Getenv("COGITORIUM_TERMINAL"); v != "" {
		cfg.Terminal = v == "1" || strings.EqualFold(v, "true")
	}
	// Same strict parse as Terminal, so COGITORIUM_EGRESS=0 is a working
	// off-switch over a file that says true.
	if v := os.Getenv("COGITORIUM_EGRESS"); v != "" {
		cfg.Egress = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("COGITORIUM_EGRESS_KEY"); v != "" {
		cfg.EgressKey = v
	}
	if v := os.Getenv("COGITORIUM_EGRESS_APPROVAL_BEARER"); v != "" {
		cfg.EgressApprovalBearer = v == "1" || strings.EqualFold(v, "true")
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
