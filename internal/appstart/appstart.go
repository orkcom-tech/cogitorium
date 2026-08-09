// Package appstart holds the decisions every shell has to make identically.
//
// There are two commands now — the server and the desktop application — and
// both must choose a sandbox and build the egress gate. Those are not
// conveniences: one of them refuses to start when the gate would be
// decorative. A second copy of a security rule is a second copy that drifts,
// and the drift is silent, so they live here once.
package appstart

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
)

// SelectSandbox decides how gears and the terminal execute. "auto" prefers
// isolation and says plainly when it cannot get it — it never pretends. A gear
// running unsandboxed holds the server's own file access, which is enough to
// read the database and the provider API keys in it.
func SelectSandbox(ctx context.Context, mode, image string) (sandbox.Runner, error) {
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

// BuildSearcher constructs the internet gate, or refuses to start.
//
// Enabling a capability that cannot work is a crash, not a warning. The
// distinction matters: absence of a capability nobody asked for is worth a log
// line, but a gate the operator explicitly switched on and which would silently
// guard nothing is the failure this whole design exists to avoid.
func BuildSearcher(cfg config.Config, sb sandbox.Runner) (*websearch.Searcher, error) {
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
