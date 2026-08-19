package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/orkcom-tech/cogitorium/internal/channel"
	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The plugins command works on the data directory, not on a running server.
//
// Every other CLI verb here is an HTTP client, and this one deliberately is
// not: a plugin is on disk before it is anything else, activation needs a
// restart anyway, and an operator whose server will not start because of a
// plugin needs to be able to disable it without the server. Requiring the
// thing you are trying to fix to be running in order to fix it is the failure
// this avoids.

func newPluginsCmds() *cobra.Command {
	var configPath, dataDir string

	root := &cobra.Command{
		Use:          "plugins",
		Short:        "Install, enable and order plugins",
		SilenceUsage: true,
	}

	// load resolves the store from the same configuration the server reads, so
	// a data directory set in config.yaml or the environment is honoured
	// without anybody repeating it on the command line.
	load := func(cmd *cobra.Command) (*plugin.Store, config.Config, error) {
		// Read the flag itself rather than asking two flag sets whether they
		// were changed. --data is persistent on the parent, and testing
		// Changed on the subcommand's own set silently answered false — which
		// sent every install to the default data directory while printing the
		// path the operator asked for. cmd.Flags() resolves inherited flags,
		// so there is one place to ask and it is the one that knows.
		override := ""
		if f := cmd.Flags().Lookup("data"); f != nil && f.Changed {
			override = f.Value.String()
		}
		cfg, err := config.Load(configPath, override)
		if err != nil {
			return nil, config.Config{}, err
		}
		// Load takes the override only to work out WHERE to look for
		// config.yaml; it does not apply it. serve does this same assignment
		// for the same reason, and leaving it out here sent every install to
		// the default directory while printing the path that was asked for.
		if override != "" {
			cfg.DataDir = override
		}
		s, err := plugin.Open(cfg.DataDir)
		return s, cfg, err
	}

	def := config.Defaults()
	root.PersistentFlags().StringVar(&configPath, "config", "",
		"path to config.yaml (default: $COGITORIUM_CONFIG, then <data-dir>/config.yaml)")
	root.PersistentFlags().StringVar(&dataDir, "data", def.DataDir,
		"data directory (SQLite DB and server-owned files)")

	var newOverride string
	newCmd := &cobra.Command{
		Use:   "new <directory>",
		Short: "Scaffold a plugin. No language, no compiler, no build step",
		Long: "Writes a plugin you can read rather than a form to fill in from documentation.\n" +
			"The default has no language and no build step, because the cheapest tier is\n" +
			"also the most common one.\n\n" +
			"--override seeds it with a real template name so you start from something\n" +
			"that renders instead of a blank file and a naming rule to look up.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			id := filepath.Base(strings.TrimSuffix(dir, string(filepath.Separator)))
			if err := plugin.Scaffold(dir, id, newOverride); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n\n  cd %s\n  cogitorium plugins dev . --watch\n", dir, dir)
			return nil
		},
	}
	newCmd.Flags().StringVar(&newOverride, "override", "",
		"seed it with an override of this template, e.g. cog.row.nav")
	root.AddCommand(newCmd)

	root.AddCommand(&cobra.Command{
		Use:          "build [directory]",
		Short:        "Package a plugin directory into a bundle",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			out, m, err := plugin.Build(dir, "")
			if err != nil {
				return err
			}
			fmt.Printf("Built %s\n  %s %s\n", out, m.ID, m.Version)
			return nil
		},
	})

	var watch bool
	devCmd := &cobra.Command{
		Use:   "dev [directory]",
		Short: "Work on a plugin from a directory, with no build step",
		Long: "Registers a directory as a development layer: no version directory, no digest,\n" +
			"no signature, and shown as such wherever plugins are listed.\n\n" +
			"--watch prints every change under the directory. It does NOT restart anything:\n" +
			"what restarts a process is whatever supervises it, and a command that killed\n" +
			"your server because you saved a file would be a worse surprise than the one it\n" +
			"saves you. Pipe it into your own loop, or press Restart now on the plugins\n" +
			"screen — the server can replace itself in place, keeping its pid.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			in, err := s.AddDev(dir)
			if err != nil {
				return err
			}
			fmt.Printf("%s %s is now a development layer at %s\n", in.Manifest.ID, in.Version, in.Dir)
			if !watch {
				fmt.Println("Restart Cogitorium to apply — from the plugins screen, or however " +
					"you started it. Add --watch to be told when a file changes.")
				return nil
			}
			return watchDir(cmd.Context(), in.Dir)
		},
	}
	devCmd.Flags().BoolVar(&watch, "watch", false, "print every change under the directory")
	root.AddCommand(devCmd)

	var refDir, fromFile string
	bakeCmd := &cobra.Command{
		Use:   "bake [bundle.zip...]",
		Short: "Materialise plugins into a derived image at build time",
		Long: "Writes bundles into an image layer so the plugin set is a property of the IMAGE\n" +
			"rather than of a volume. Wipe the volume, land on a fresh node, or run with no\n" +
			"volume at all, and the same plugins come up.\n\n" +
			"Baked plugins are approved by whoever built the image, because choosing them\n" +
			"IS that decision — asking the operator who later runs it to approve them again\n" +
			"would be asking them to ratify a choice they cannot change without rebuilding.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundles := args
			if fromFile != "" {
				b, err := os.ReadFile(fromFile)
				if err != nil {
					return err
				}
				for _, line := range strings.Split(string(b), "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						bundles = append(bundles, line)
					}
				}
			}
			if len(bundles) == 0 {
				return fmt.Errorf("name at least one bundle, or -f a file listing them")
			}
			baked, err := plugin.Bake(refDir, bundles)
			if err != nil {
				return err
			}
			dir := refDir
			if dir == "" {
				dir = plugin.RefDir
			}
			fmt.Printf("Baked %d plugin(s) into %s\n", len(baked), dir)
			for _, in := range baked {
				fmt.Printf("  %s %s\n", in.ID, in.Version)
			}
			return nil
		},
	}
	bakeCmd.Flags().StringVar(&refDir, "ref", "", "the image tree to write into (default "+plugin.RefDir+")")
	bakeCmd.Flags().StringVarP(&fromFile, "file", "f", "", "a file listing bundles, one per line")
	root.AddCommand(bakeCmd)

	var seedRef string
	seedCmd := &cobra.Command{
		Use:   "seed",
		Short: "Copy the image's baked plugins into the data directory",
		Long: "Run on EVERY start, not the first. That is what makes the plugin set a property\n" +
			"of the image: a first-start-only seed comes up empty the moment somebody\n" +
			"recreates the container against a volume that already exists.\n\n" +
			"Only plugins are copied. Runtimes are used where they are, read-only.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := load(cmd)
			if err != nil {
				return err
			}
			_ = s
			if err := plugin.CheckRef(seedRef); err != nil {
				// Reported rather than fatal: a server that refused to start
				// because a baked plugin was unreadable would take the whole
				// product away over an extra.
				fmt.Fprintln(os.Stderr, "cogitorium: "+err.Error())
			}
			seeded, err := plugin.Seed(seedRef, cfg.DataDir)
			if err != nil {
				return err
			}
			if len(seeded) > 0 {
				fmt.Printf("Seeded %s\n", strings.Join(seeded, ", "))
			}
			return nil
		},
	}
	seedCmd.Flags().StringVar(&seedRef, "ref", "", "the image tree to read from (default "+plugin.RefDir+")")
	root.AddCommand(seedCmd)

	var catalogBase string
	checkCatalog := &cobra.Command{
		Use:   "check-catalog <plugins.json>",
		Short: "Validate a catalog file and say whether a submission may merge itself",
		Long: "Reads a catalog the way a client reads it: every entry validated, ids unique,\n" +
			"no field nobody implements.\n\n" +
			"With --base, it also reports what a submission did. Additions may merge on\n" +
			"green CI — listing your own plugin takes nothing from anybody. An edit or a\n" +
			"removal touches an entry somebody already installed, so it exits non-zero and\n" +
			"waits for a person: the id in a public file is not proof of who owns it, and\n" +
			"the author of a pull request is whoever opened it.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			after, err := plugin.ReadCatalog(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s: %d entries, all readable\n", args[0], len(after))
			if catalogBase == "" {
				return nil
			}

			before, err := plugin.ReadCatalog(catalogBase)
			if err != nil {
				return fmt.Errorf("the catalog as it stands is unreadable, which is not this submission's fault: %w", err)
			}
			c := plugin.Diff(before, after)
			for _, e := range c.Added {
				fmt.Printf("  + %s (%s) -> %s\n", e.ID, e.Author, e.Repo)
			}
			for _, e := range c.Edited {
				fmt.Printf("  ~ %s: %s\n", e.After.ID, strings.Join(e.Fields, ", "))
			}
			for _, e := range c.Removed {
				fmt.Printf("  - %s\n", e.ID)
			}
			if ok, why := c.AutoMergeable(); !ok {
				return fmt.Errorf("%s", why)
			}
			fmt.Println("\nOK — this may merge on green CI")
			return nil
		},
	}
	checkCatalog.Flags().StringVar(&catalogBase, "base", "",
		"the catalog as it stands, to report what this submission changed")
	root.AddCommand(checkCatalog)

	root.AddCommand(&cobra.Command{
		Use:   "check-bundle <bundle.zip>",
		Short: "Validate a bundle without installing it. What the catalog's CI runs",
		Long: "Unpacks a bundle somewhere temporary, validates its manifest, and composes its\n" +
			"templates against this build — the same code the server runs at boot.\n\n" +
			"One implementation, so a submission cannot pass CI and then fail to load on the\n" +
			"first machine that tries it.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tmp, err := os.MkdirTemp("", "cogitorium-check-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)

			s, err := plugin.Open(tmp)
			if err != nil {
				return err
			}
			in, digest, err := s.Install(args[0])
			if err != nil {
				return err
			}

			sources, err := view.Sources([]plugin.Installed{in})
			if err != nil {
				return err
			}
			set, report, err := view.Boot(view.Funcs(), view.Core(), sources, view.CoreModels())
			if err != nil {
				return err
			}
			for _, d := range report.Disabled {
				return fmt.Errorf("%s would not load: %s", d.ID, d.Reason())
			}

			fmt.Printf("%s %s\n  %s\n", in.Manifest.ID, in.Version, digest)
			for _, e := range set.Ledger().For(in.ID) {
				switch e.Action {
				case view.Overrides:
					fmt.Printf("  overrides %s\n", e.Name)
				case view.Extends:
					fmt.Printf("  extends   %s\n", e.Name)
				case view.Dangling:
					// Not a failure: the plugin it extends may simply not be
					// installed here. Named so a reviewer sees it.
					fmt.Printf("  INERT     %s — nothing installed owns that namespace\n", e.Name)
				default:
					fmt.Printf("  adds      %s\n", e.Name)
				}
			}
			printDeclarations(in.Manifest)
			fmt.Println("\nOK")
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "list",
		Short:        "List installed plugins, in layer order",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := load(cmd)
			if err != nil {
				return err
			}
			all, err := s.List()
			if err != nil {
				return err
			}
			if len(all) == 0 {
				fmt.Printf("No plugins installed. They live in %s\n", s.Root())
				return nil
			}

			caps := plugin.Capabilities{Profile: channel.Detect(cfg.DataDir)}
			table("ID\tVERSION\tSTATE\tTIER\tNAME", func(w *tabwriter.Writer) {
				for _, in := range all {
					if in.Broken != nil {
						fmt.Fprintf(w, "%s\t-\tBROKEN\t-\t%v\n", in.ID, in.Broken)
						continue
					}
					state := "installed"
					switch {
					case in.Dev:
						state = fmt.Sprintf("development #%d", in.Order+1)
					case in.Enabled:
						state = fmt.Sprintf("enabled #%d", in.Order+1)
					case in.Pending != "":
						state = "pending approval"
					}
					r := plugin.Resolve(in.Manifest, caps)
					tier := string(r.Tier)
					if !r.Available {
						tier += " (unavailable)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
						in.Manifest.ID, in.Version, state, tier, in.Manifest.Name)
				}
			})
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "install <bundle.zip>",
		Short:        "Unpack a plugin bundle. Does not enable it",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := load(cmd)
			if err != nil {
				return err
			}
			in, digest, err := s.Install(args[0])
			if err != nil {
				return err
			}

			r := plugin.Resolve(in.Manifest, plugin.Capabilities{Profile: channel.Detect(cfg.DataDir)})
			fmt.Printf("Installed %s %s\n  %s\n", in.Manifest.ID, in.Version, digest)
			if r.Note != "" {
				fmt.Printf("  %s: %s\n", r.Tier, r.Note)
			}
			if !r.Available {
				// Said now rather than at boot. The whole reason resolution
				// fetches nothing is so this sentence arrives before anybody
				// has waited for a download or wondered why a page is blank.
				fmt.Printf("\n  This install cannot run it:\n  %s\n", r.Refusal)
			}
			// What it declares is printed, because enabling is the moment a
			// person is deciding, and deciding on a list they have to go and
			// find is deciding on a list they will not read.
			printDeclarations(in.Manifest)
			fmt.Printf("\nIt is installed and NOT approved. Nothing of its runs until somebody "+
				"decides about it:\n  cogitorium plugins approve %s\n", in.Manifest.ID)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "approve <id>",
		Short: "Approve exactly what is installed, so it can be enabled",
		Long: "Approval covers exact content, not a name. A plugin whose bytes change returns\n" +
			"to pending — a decision made about code somebody read is not a decision about\n" +
			"code they have not.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			in, err := s.Get(args[0])
			if err != nil {
				return err
			}
			// Printed before the decision, because deciding on a list somebody
			// has to go and find is deciding on a list they will not read.
			printDeclarations(in.Manifest)
			a, err := s.Approve(args[0], operatorName())
			if err != nil {
				return err
			}
			fmt.Printf("\nApproved %s %s\n  %s\n\nEnable it with:\n  cogitorium plugins enable %s\n",
				args[0], a.Version, a.Digest, args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "revoke <id>",
		Short:        "Withdraw approval and disable the plugin",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			if err := s.Revoke(args[0]); err != nil {
				return err
			}
			fmt.Printf("%s is no longer approved and has been disabled. Restart Cogitorium to apply.\n",
				args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "enable <id>",
		Short:        "Add a plugin to the end of the enable list",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			if err := s.Enable(args[0]); err != nil {
				return err
			}
			fmt.Printf("Enabled %s. Restart Cogitorium to apply.\n", args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "disable <id>",
		Short:        "Take a plugin out of the enable list, leaving it installed",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			if err := s.Disable(args[0]); err != nil {
				return err
			}
			fmt.Printf("Disabled %s. Restart Cogitorium to apply.\n", args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "order [id...]",
		Short: "Show or set the enable order. Position is precedence",
		Long: "Position in this list is precedence: a plugin later in it renders instead of one\n" +
			"earlier when they define the same template name. With no arguments the current\n" +
			"order is printed.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				order, err := s.Order()
				if err != nil {
					return err
				}
				if len(order) == 0 {
					fmt.Println("Nothing is enabled.")
					return nil
				}
				for i, id := range order {
					fmt.Printf("%d. %s\n", i+1, id)
				}
				return nil
			}
			if err := s.Reorder(args); err != nil {
				return err
			}
			fmt.Printf("Order set: %s. Restart Cogitorium to apply.\n", strings.Join(args, ", "))
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:          "remove <id>",
		Short:        "Delete an installed plugin",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			if err := s.Remove(args[0]); err != nil {
				return err
			}
			fmt.Printf("Removed %s. Restart Cogitorium to apply.\n", args[0])
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Compose the enabled plugins and report what each one does",
		Long: "Builds the template set exactly as the server would at boot, and reports what\n" +
			"each plugin actually overrides — computed from the templates it ships, not from\n" +
			"what its manifest claims.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, _, err := load(cmd)
			if err != nil {
				return err
			}
			enabled, err := s.Enabled()
			if err != nil {
				return err
			}
			sources, err := view.Sources(enabled)
			if err != nil {
				return err
			}
			set, report, err := view.Boot(view.Funcs(), view.Core(), sources, view.CoreModels())
			if err != nil {
				return err
			}

			if len(report.Loaded) == 0 && len(report.Disabled) == 0 {
				fmt.Println("Nothing is enabled. The host's own templates compose cleanly.")
				return nil
			}
			for _, id := range report.Loaded {
				fmt.Printf("%s\n", id)
				for _, e := range set.Ledger().For(id) {
					switch e.Action {
					case view.Overrides:
						fmt.Printf("  overrides %s (was %s)\n", e.Name, e.Took)
					case view.Extends:
						fmt.Printf("  extends   %s\n", e.Name)
					case view.Dangling:
						fmt.Printf("  INERT     %s — nothing installed owns that namespace\n", e.Name)
					default:
						fmt.Printf("  adds      %s\n", e.Name)
					}
				}
			}
			for _, d := range report.Disabled {
				fmt.Fprintf(os.Stderr, "\nDISABLED %s\n  %s\n", d.ID, d.Reason())
			}
			if len(report.Disabled) > 0 {
				return fmt.Errorf("%d plugin(s) would not load", len(report.Disabled))
			}
			return nil
		},
	})

	return root
}

// operatorName is who a decision is recorded against on the command line.
// The shell's own idea of who is running, because a CLI has no session and
// pretending it does would put a fixed word in an audit trail.
func operatorName() string {
	for _, k := range []string{"COGITORIUM_OPERATOR", "USER", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return "console"
}

// printDeclarations shows what a manifest asks for, grouped the way the
// approval screen groups it: what it changes, then what it wants.
func printDeclarations(m plugin.Manifest) {
	if len(m.Pages) > 0 || len(m.Nav) > 0 || len(m.Overrides) > 0 {
		fmt.Println("\n  It adds to the interface:")
		for _, p := range m.Pages {
			auth := p.Auth
			if auth == "" {
				auth = "token"
			}
			line := fmt.Sprintf("    page %s (%s)", p.Path, auth)
			if auth == "none" {
				line += "  ← reachable without signing in"
			}
			fmt.Println(line)
		}
		for _, n := range m.Nav {
			fmt.Printf("    rail entry %q → %s\n", n.Label, n.Href)
		}
		for _, o := range m.Overrides {
			fmt.Printf("    declares an override of %s\n", o)
		}
	}
	if len(m.Hosts) > 0 || len(m.Secrets) > 0 || len(m.API) > 0 {
		fmt.Println("\n  It asks for:")
		for _, h := range m.Hosts {
			fmt.Printf("    network  %s\n", h)
		}
		for _, s := range m.Secrets {
			fmt.Printf("    secret   %s (the name; the value is never handed over)\n", s)
		}
		for _, a := range m.API {
			fmt.Printf("    api      %s\n", a)
		}
	}
}

// watchDir reports changes under a development layer.
//
// It polls rather than using a filesystem notification API, and that is a
// deliberate trade: notifications differ on every platform this ships to and
// would be a dependency and six behaviours, while a plugin directory is a
// handful of small files and a second-resolution poll is imperceptible to the
// person editing them.
//
// It does not restart the server itself. What restarts a process is the thing
// supervising it — systemd, compose, the kubelet, or the developer's own
// terminal — and a command that killed somebody's server because a file was
// saved would be a worse surprise than the one it saves them.
func watchDir(ctx context.Context, dir string) error {
	fmt.Println("Watching for changes. Restart Cogitorium to pick them up; Ctrl-C to stop.")
	prev, err := snapshot(dir)
	if err != nil {
		return err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			cur, err := snapshot(dir)
			if err != nil {
				// A directory that vanished mid-edit is worth saying out loud
				// rather than exiting silently on.
				fmt.Fprintf(os.Stderr, "watch: %v\n", err)
				continue
			}
			for _, line := range diff(prev, cur) {
				fmt.Println(line)
			}
			prev = cur
		}
	}
}

func snapshot(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return out, err
}

func diff(before, after map[string]string) []string {
	var out []string
	for name, stamp := range after {
		switch prev, existed := before[name]; {
		case !existed:
			out = append(out, "added   "+name)
		case prev != stamp:
			out = append(out, "changed "+name)
		}
	}
	for name := range before {
		if _, still := after[name]; !still {
			out = append(out, "removed "+name)
		}
	}
	sort.Strings(out)
	return out
}
