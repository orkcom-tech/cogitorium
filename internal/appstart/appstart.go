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
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
)

// SelectSandbox decides how gears and the terminal execute. "auto" prefers
// isolation and says plainly when it cannot get it — it never pretends. A gear
// running unsandboxed holds the server's own file access, which is enough to
// read the database and the provider API keys in it.
func SelectSandbox(ctx context.Context, mode, image, runtime string) (sandbox.Runner, error) {
	switch mode {
	case "subprocess":
		if runtime != "" {
			// Refused rather than ignored. A sandbox_runtime set beside
			// sandbox: subprocess is somebody who believes they have hardened
			// isolation and has in fact switched isolation off — the one
			// misconfiguration here that reads as the opposite of what it is.
			return nil, errors.New("sandbox_runtime is set but sandbox is \"subprocess\", which runs gears " +
				"with this server's own file access and no container at all. A runtime cannot harden something " +
				"that is not being isolated: either set sandbox to docker or auto, or remove sandbox_runtime")
		}
		slog.Warn("sandbox disabled by configuration: gears run with this server's file access")
		return nil, nil
	case "docker", "auto", "":
		d := sandbox.NewDocker(image, runtime)
		if d != nil && d.Available(ctx) {
			// A runtime the daemon does not have is a startup failure whether
			// or not the mode was "auto": falling back to the default runtime
			// would hand somebody who asked for gVisor a plain runc container
			// and log it as success.
			if err := d.CheckRuntime(ctx); err != nil {
				return nil, err
			}
			slog.Info("gears run sandboxed", "backend", d.Name())
			// Fetch the image now, in the background, so the first gear does
			// not pay for it inside its own timeout — a sixty-second gear
			// failing because it spent ninety pulling an image is a failure
			// that says nothing about the gear.
			//
			// Background because startup must not block on a registry, and
			// best-effort because a pull that fails here is not fatal: the run
			// will pull it itself, slowly, exactly as it did before.
			go func() {
				if err := d.Pull(context.WithoutCancel(ctx)); err != nil {
					slog.Info("could not pre-fetch the sandbox image; the first gear will fetch it", "err", err)
					return
				}
				slog.Info("sandbox image ready", "image", d.Image)
			}()
			return d, nil
		}
		if mode == "docker" {
			return nil, errors.New("sandbox: docker was requested but the daemon does not answer")
		}
		if runtime != "" {
			return nil, errors.New("sandbox_runtime is set but Docker does not answer, so there is no daemon " +
				"to select a runtime on. Start Docker, or remove sandbox_runtime")
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

// BuildGearNet opens the outward gate for gears: the proxy a granted gear's
// traffic is carried through, checked against the destinations the operator
// granted, and written down.
//
// It is built unconditionally rather than only when some gear happens to hold a
// grant, because the alternative is opening a listening socket at the moment a
// pipeline first needs one — so a port already taken, or an address the machine
// does not hold, becomes a gear failing in production instead of a server
// refusing to start. The gate grants nothing by existing: without a ticket, and
// a ticket exists only for the length of a granted run, it answers 407.
//
// Unlike the search gate, an absent sandbox is NOT a refusal to start here, and
// the difference is worth stating. Refusing would take gears away from every
// install without Docker, which is most laptops, to protect a grant that is off
// by default. What it earns instead is a sentence: without a sandbox a gear
// already holds the server's own network, so the grant stops being a boundary
// and becomes only a record — and an operator running that way should know
// which of the two they have.
//
// The caller closes it.
func BuildGearNet(cfg config.Config, db *sql.DB, sb sandbox.Runner) (*gearnet.Gate, error) {
	// Where to bind depends on the sandbox: a container reaches this machine at
	// an address that is not the loopback on Linux, and a gate the gear cannot
	// dial makes the grant useless.
	gate, err := gearnet.New(db, gearnet.ListenFor(context.Background(), cfg.GearProxyListen, sb))
	if err != nil {
		return nil, err
	}
	if sb == nil {
		slog.Warn("gears are not sandboxed, so a gear already holds this server's network whether or not " +
			"it was granted one: the destinations an operator grants are recorded and checked for code that " +
			"uses the proxy it is given, and enforced on nothing. Install Docker or set sandbox: docker")
	}
	return gate, nil
}

// BuildSecrets constructs the lookup that turns the names a gear declares into
// the values it is given, and refuses to start on a source that cannot work.
//
// A configured directory that cannot be read is a startup error for the same
// reason a decorative egress gate is: the operator asked for a source, and one
// that silently supplies nothing turns into a gear failing at three in the
// morning with a message about a name.
//
// A missing COGITORIUM_SECRET_KEY is NOT an error — it is the ordinary state of
// an install that keeps nothing sensitive in its own database. It becomes worth
// saying only when there are already secrets in there that it can no longer
// open, which is a question that has to be asked of the database, here, once.
func BuildSecrets(ctx context.Context, cfg config.Config, db *sql.DB) (*secrets.Resolver, error) {
	var key *secrets.Key
	if cfg.SecretKey != "" {
		k, err := secrets.NewKey(cfg.SecretKey)
		if err != nil {
			return nil, err
		}
		key = k
	}

	store := secrets.NewStore(db, key)
	resolver, err := secrets.NewResolver(store, cfg.VariablesDir, cfg.SecretsDir)
	if err != nil {
		return nil, err
	}

	if key == nil {
		n, err := store.CountSealed(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			slog.Warn("this install holds secrets it cannot read: COGITORIUM_SECRET_KEY is not set, "+
				"so every gear that asks for one of them will be refused. Set it to the key this install was using, or set those names again",
				"secrets", n)
		}
	}
	if v, s := resolver.Sources(); v != "" || s != "" {
		slog.Info("named values are also read from disk, one file per name",
			"variables_dir", v, "secrets_dir", s)
	}
	return resolver, nil
}
