package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/server"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
	"github.com/spf13/cobra"
)

// selectSandbox decides how gears and the terminal execute. "auto" prefers
// isolation and says plainly when it cannot get it — it never pretends. A
// gear running unsandboxed holds the server's own file access, which is
// enough to read the database and the provider API keys in it.
func selectSandbox(ctx context.Context, mode, image string) (sandbox.Runner, error) {
	switch mode {
	case "subprocess":
		slog.Warn("sandbox disabled by configuration: gears run with this server's file access")
		return nil, nil
	case "docker", "auto", "":
		d := sandbox.NewDocker(image)
		if d != nil && d.Available(ctx) {
			slog.Info("gears run sandboxed", "backend", d.Name())
			return d, nil
		}
		if mode == "docker" {
			return nil, errors.New("sandbox: docker was requested but the daemon does not answer")
		}
		// A compensating control nobody is told about is not one: this branch
		// silently downgraded to unsandboxed execution before.
		slog.Warn("docker did not answer; gears will run unsandboxed with this server's file access")
		return nil, nil
	default:
		return nil, fmt.Errorf("sandbox must be auto, docker or subprocess (got %q)", mode)
	}
}

func newServeCmd() *cobra.Command {
	var (
		configPath string
		listen     string
		dataDir    string
		logLevel   string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Cogitorium server (API + web UI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataOverride := ""
			if cmd.Flags().Changed("data") {
				dataOverride = dataDir
			}
			cfg, err := config.Load(configPath, dataOverride)
			if err != nil {
				return err
			}
			// Flags win over env and file, but only when actually set.
			if cmd.Flags().Changed("listen") {
				cfg.Listen = listen
			}
			if cmd.Flags().Changed("data") {
				cfg.DataDir = dataDir
			}
			if cmd.Flags().Changed("log-level") {
				cfg.LogLevel = logLevel
			}

			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: cfg.SlogLevel(),
			})))
			slog.Info("starting cogitorium", "listen", cfg.Listen, "data_dir", cfg.DataDir, "log_level", cfg.LogLevel)

			db, err := store.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer db.Close()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			sb, err := selectSandbox(ctx, cfg.Sandbox, cfg.SandboxImage)
			if err != nil {
				return err
			}

			searcher, err := buildSearcher(cfg, sb)
			if err != nil {
				return err
			}

			srv := server.New(cfg, db, sb, searcher)
			if err := srv.Bootstrap(ctx); err != nil {
				return err
			}
			return srv.Run(ctx)
		},
	}

	def := config.Defaults()
	cmd.Flags().StringVar(&configPath, "config", "", "path to config.yaml (default: $COGITORIUM_CONFIG, then <data-dir>/config.yaml)")
	cmd.Flags().StringVar(&listen, "listen", def.Listen, "HTTP listen address")
	cmd.Flags().StringVar(&dataDir, "data", def.DataDir, "data directory (SQLite DB and server-owned files)")
	cmd.Flags().StringVar(&logLevel, "log-level", def.LogLevel, "log level: debug|info|warn|error")
	return cmd
}

// buildSearcher constructs the internet gate, or refuses to start.
//
// Enabling a capability that cannot work is a crash, not a warning. The
// distinction matters: absence of a capability nobody asked for is worth a
// log line, but a gate the operator explicitly switched on and which would
// silently guard nothing is the failure this whole design exists to avoid.
func buildSearcher(cfg config.Config, sb sandbox.Runner) (*websearch.Searcher, error) {
	if !cfg.Egress {
		return nil, nil
	}
	// Unsandboxed gears already run with this server's file access: they can
	// rewrite config.yaml and the grants table, so the gate would be theatre.
	if sb == nil {
		return nil, errors.New("egress is enabled but gears are not sandboxed. An unsandboxed gear runs " +
			"with this server's file access and can rewrite the configuration and the grants table, so the " +
			"gate would be decorative. Install Docker, or set egress: false.")
	}
	s, err := websearch.New(cfg.Listen, cfg.EgressKey)
	if err != nil {
		return nil, fmt.Errorf("egress is enabled but cannot be built: %w", err)
	}
	slog.Warn("the outward gate is ON: agents may ask to search the web",
		"destination", websearch.Destination(),
		"note", "every search still stops the turn and waits for an operator to approve that exact query")
	return s, nil
}
