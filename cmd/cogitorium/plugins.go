package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

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
					if in.Enabled {
						state = fmt.Sprintf("enabled #%d", in.Order+1)
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
			fmt.Printf("\nIt is installed and OFF. Enable it with:\n  cogitorium plugins enable %s\n",
				in.Manifest.ID)
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
