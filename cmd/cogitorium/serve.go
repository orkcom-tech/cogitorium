package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/orkcom-tech/cogitorium/internal/appstart"
	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/server"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/spf13/cobra"
)

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

			sb, err := appstart.SelectSandbox(ctx, cfg.Sandbox, cfg.SandboxImage, cfg.SandboxRuntime)
			if err != nil {
				return err
			}

			searcher, err := appstart.BuildSearcher(cfg, sb)
			if err != nil {
				return err
			}

			env, err := appstart.BuildSecrets(ctx, cfg, db)
			if err != nil {
				return err
			}

			gate, err := appstart.BuildGearNet(cfg, db, sb)
			if err != nil {
				return err
			}
			defer gate.Close()

			srv := server.New(cfg, db, sb, searcher, env, gate)
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
