package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/orkcom-tech/cogitorium/internal/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The command line, over the same HTTP API the interface uses.
//
// Wrapping rather than building: every one of these calls a route that already
// exists and is already described in docs/openapi.yaml. What the commands add
// is the two things a terminal wants and a browser does not — an exit code that
// means something to a shell, and output narrow enough to pipe.
//
// Deliberately not here: creating agents, drawing wires, approving gears. Those
// are decisions with a canvas and a source listing beside them, and a flag is a
// worse place to make them than a screen that shows what is being decided.

// clientFlags are shared by every command so the precedence — flag, then
// environment, then default — is stated once.
type clientFlags struct {
	server string
	token  string
}

func (f *clientFlags) bind(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.server, "server", "", "Cogitorium's address (default $COGITORIUM_URL, then "+client.DefaultURL+")")
	cmd.PersistentFlags().StringVar(&f.token, "token", "", "token to authenticate with (default $COGITORIUM_TOKEN)")
}

func (f *clientFlags) client() *client.Client { return client.New(f.server, f.token) }

// readPassword takes one without putting it in the terminal's scrollback, and
// falls back to plain stdin when there is no terminal to suppress echo on —
// which is the case in a pipe, where there is no echo to begin with.
func readPassword(msg io.Writer) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(msg, "password: ")
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(msg)
		if err != nil {
			return "", fmt.Errorf("could not read the password: %w", err)
		}
		return string(raw), nil
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("could not read the password from stdin: %w", err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// out writes a table, or the raw JSON when asked. Both, because a person wants
// columns and a script wants to keep every field this client does not model.
func table(header string, rows func(*tabwriter.Writer)) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if header != "" {
		fmt.Fprintln(w, header)
	}
	rows(w)
	_ = w.Flush()
}

func newCLICmds() []*cobra.Command {
	var f clientFlags

	// login exists because the CLI stopped working without a token.
	//
	// It did work: a call from the server's own machine was served as the
	// admin, so `cogitorium queue list` needed no credential on a laptop. That
	// shortcut is gone, and without this command the only way to get a token
	// for the terminal client would be to reach for curl — a documented
	// workflow that starts by telling people not to use the tool.
	var loginUser string
	login := &cobra.Command{
		Use:   "login",
		Short: "Exchange a password for a token, and print it",
		Long: "Prints a token on stdout and nothing else, so it can be captured:\n\n" +
			"    export COGITORIUM_TOKEN=$(cogitorium login --user admin)\n\n" +
			"The password is read from the terminal without echoing. When stdin is not a\n" +
			"terminal it is read from there instead, so a script can pipe one in.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := readPassword(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			var out struct {
				Token string `json:"token"`
			}
			body := map[string]string{"name": loginUser, "password": password}
			// A client with no token: this is the route that issues one, and
			// sending a stale one would be the only way to fail here twice.
			c := client.New(f.server, "-")
			c.Token = ""
			if err := c.Do(cmd.Context(), http.MethodPost, "/api/v1/login", body, &out); err != nil {
				return err
			}
			// stdout carries the token alone, so $(…) captures a token and not
			// a sentence. Anything for a person goes to stderr.
			fmt.Fprintln(cmd.OutOrStdout(), out.Token)
			return nil
		},
	}
	login.Flags().StringVar(&loginUser, "user", "admin", "the account to sign in as")
	login.Flags().StringVar(&f.server, "server", "", "Cogitorium's address (default $COGITORIUM_URL, then "+client.DefaultURL+")")

	workspaces := &cobra.Command{
		Use:          "workspaces",
		Short:        "List the workspaces this token can see, or move one between installs",
		Aliases:      []string{"ws"},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var all []client.Workspace
			if err := f.client().Do(cmd.Context(), http.MethodGet, "/api/v1/workspaces", nil, &all); err != nil {
				return err
			}
			table("ID\tNAME\tDESCRIPTION", func(w *tabwriter.Writer) {
				for _, ws := range all {
					fmt.Fprintf(w, "%d\t%s\t%s\n", ws.ID, ws.Name, ws.Description)
				}
			})
			return nil
		},
	}

	var exGears, exContext bool
	var exOut string
	export := &cobra.Command{
		Use:   "export <id>",
		Short: "Write a workspace bundle to a file, or to stdout",
		Long: `Export a workspace as a bundle.

Agents and their wiring always travel. Gears and context are opt-in, because
they are the two parts that carry somebody else's code and somebody else's
documents, and a bundle that quietly included them would be a surprise on the
receiving side.

Imported gears arrive pending, wherever the bundle came from.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/workspaces/" + args[0] + "/export" +
				fmt.Sprintf("?gears=%s&context=%s", flag01(exGears), flag01(exContext))
			var raw json.RawMessage
			if err := f.client().Do(cmd.Context(), http.MethodGet, path, nil, &raw); err != nil {
				return err
			}
			if exOut == "" || exOut == "-" {
				fmt.Println(string(raw))
				return nil
			}
			if err := os.WriteFile(exOut, append(raw, '\n'), 0o600); err != nil {
				return err
			}
			// 0600 rather than 0644: a bundle can carry context files and gear
			// source, and this writes wherever the operator happens to stand.
			fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", exOut, len(raw))
			return nil
		},
	}
	export.Flags().BoolVar(&exGears, "gears", false, "include the gears this workspace uses")
	export.Flags().BoolVar(&exContext, "context", false, "include the context documents")
	export.Flags().StringVarP(&exOut, "out", "o", "", "write here instead of stdout")
	workspaces.AddCommand(export)

	var imName string
	var imGears, imContext bool
	imp := &cobra.Command{
		Use:          "import <file>",
		Short:        "Build a new workspace from a bundle file",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if !json.Valid(raw) {
				return fmt.Errorf("%s is not JSON — a bundle is the document `cogitorium workspaces export` writes", args[0])
			}
			var res client.ImportResult
			if err := f.client().Do(cmd.Context(), http.MethodPost, "/api/v1/workspaces/import", map[string]any{
				"name":            imName,
				"bundle":          json.RawMessage(raw),
				"include_gears":   imGears,
				"include_context": imContext,
			}, &res); err != nil {
				return err
			}
			fmt.Printf("workspace %d %q — %d agents, %d wires, %d context files\n",
				res.Workspace.ID, res.Workspace.Name, res.Agents, res.Wires, res.ContextFiles)
			if len(res.GearsImported) > 0 {
				fmt.Printf("gears: %s (pending — approve them before anything can run them)\n",
					strings.Join(res.GearsImported, ", "))
			}
			// Skips and unresolved models are the half worth printing. A bundle
			// whose gears were all skipped imported "successfully" and produced
			// a workspace that cannot do its work.
			for _, s := range res.GearsSkipped {
				fmt.Fprintf(os.Stderr, "skipped gear %s: %s\n", s.Name, s.Why)
			}
			for _, m := range res.UnresolvedModels {
				fmt.Fprintf(os.Stderr, "agent %s wants %s/%s, which this install does not have\n",
					m.Agent, m.ProviderType, m.ModelName)
			}
			return nil
		},
	}
	imp.Flags().StringVar(&imName, "name", "", "name for the new workspace (default the bundle's)")
	imp.Flags().BoolVar(&imGears, "gears", false, "import the gears the bundle carries")
	imp.Flags().BoolVar(&imContext, "context", false, "import the context documents")
	workspaces.AddCommand(imp)

	gears := &cobra.Command{Use: "gears", Short: "The tool catalog", SilenceUsage: true}
	gears.AddCommand(&cobra.Command{
		Use:          "list",
		Short:        "List every gear and its approval status",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var all []client.Gear
			if err := f.client().Do(cmd.Context(), http.MethodGet, "/api/v1/gears", nil, &all); err != nil {
				return err
			}
			table("ID\tNAME\tSTATUS\tRUNTIME\tV\tDESCRIPTION", func(w *tabwriter.Writer) {
				for _, g := range all {
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\n", g.ID, g.Name, g.Status, g.Runtime, g.Version, g.Description)
				}
			})
			return nil
		},
	})

	var gearArgs string
	run := &cobra.Command{
		Use:   "run <name-or-id>",
		Short: "Run an approved gear",
		Long: `Run an approved gear and print what it produced.

This is the invoke route, so the approval gate holds: a gear that is pending or
disabled is refused rather than run. The exit code is the gear's own, so a
shell can branch on it.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := f.client()
			id, err := gearID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{}
			if strings.TrimSpace(gearArgs) != "" {
				var parsed any
				if err := json.Unmarshal([]byte(gearArgs), &parsed); err != nil {
					return fmt.Errorf("--args is not JSON: %w", err)
				}
				body["args"] = parsed
			}
			var res client.GearResult
			if err := c.Do(cmd.Context(), http.MethodPost,
				"/api/v1/gears/"+strconv.FormatInt(id, 10)+"/invoke", body, &res); err != nil {
				return err
			}
			if res.Stdout != "" {
				fmt.Print(res.Stdout)
			}
			if res.Stderr != "" {
				fmt.Fprint(os.Stderr, res.Stderr)
			}
			if res.Error != "" {
				return fmt.Errorf("%s", res.Error)
			}
			if res.TimedOut {
				return fmt.Errorf("the gear hit its timeout and was stopped")
			}
			if res.ExitCode != 0 {
				// The gear's own exit code, not a generic failure: a shell
				// branching on this is branching on what the gear said.
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
	run.Flags().StringVar(&gearArgs, "args", "", "the gear's arguments, as JSON")
	gears.AddCommand(run)

	receivers := &cobra.Command{Use: "receivers", Short: "The doors into a workspace", Aliases: []string{"inlets"}, SilenceUsage: true}
	var rcvWS int64
	list := &cobra.Command{
		Use:          "list",
		Short:        "List a workspace's receivers and their tasks",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rcvWS == 0 {
				return fmt.Errorf("which workspace? pass --workspace <id>; `cogitorium workspaces` lists them")
			}
			var all []client.Inlet
			if err := f.client().Do(cmd.Context(), http.MethodGet,
				"/api/v1/workspaces/"+strconv.FormatInt(rcvWS, 10)+"/inlets", nil, &all); err != nil {
				return err
			}
			table("ADDRESS\tKEY\tTASK\tACCEPTS\tAGENT", func(w *tabwriter.Writer) {
				for _, in := range all {
					key := "none issued"
					if in.HasKey {
						key = "issued"
					}
					if len(in.Tasks) == 0 {
						fmt.Fprintf(w, "%s\t%s\t(none — answers 404)\t\t\n", in.Address, key)
						continue
					}
					for _, t := range in.Tasks {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", in.Address, key, t.Name, t.Accepts, t.AgentName)
					}
				}
			})
			return nil
		},
	}
	list.Flags().Int64Var(&rcvWS, "workspace", 0, "the workspace to list")
	receivers.AddCommand(list)

	var deliverKey, deliverData string
	var deliverAsync bool
	deliver := &cobra.Command{
		Use:   "deliver <address>/<task>",
		Short: "Post a JSON payload to a receiver task",
		Long: `Deliver to a receiver, with that receiver's own key.

The key is the receiver's, not the management token: a door's credential opens
that door and nothing else, and the ledger records which one was used.

By default the call is held open until the work finishes, which is what you want
at a prompt. With --async it returns a run number straight away and the work
carries on; cogitorium run <id> reads it back.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			address, task, ok := strings.Cut(args[0], "/")
			if !ok || address == "" || task == "" {
				return fmt.Errorf("give it as <address>/<task>, for example sites/ingest-page")
			}
			if deliverKey == "" {
				deliverKey = os.Getenv("COGITORIUM_INLET_KEY")
			}
			if deliverKey == "" {
				return fmt.Errorf("a receiver needs its own key: pass --key, or set COGITORIUM_INLET_KEY")
			}
			payload := []byte(deliverData)
			if strings.TrimSpace(deliverData) == "" {
				payload = []byte("{}")
			}
			if !json.Valid(payload) {
				return fmt.Errorf("--data is not JSON")
			}
			raw, status, err := f.client().Deliver(cmd.Context(), address, task, deliverKey, payload, deliverAsync)
			if err != nil {
				return err
			}
			if status >= 400 {
				return fmt.Errorf("%d: %s", status, client.Message(raw))
			}
			// The whole body on success, because it carries the run number and
			// the record of what the agent did, and a script wants both.
			fmt.Println(strings.TrimSpace(string(raw)))
			return nil
		},
	}
	deliver.Flags().StringVar(&deliverKey, "key", "", "the receiver's key (default $COGITORIUM_INLET_KEY)")
	deliver.Flags().StringVar(&deliverData, "data", "", "the JSON payload")
	deliver.Flags().BoolVar(&deliverAsync, "async", false, "take a run number now instead of waiting for the answer")
	receivers.AddCommand(deliver)

	queue := &cobra.Command{Use: "queue", Short: "What is running and what is waiting", SilenceUsage: true}
	var queueWS int64
	qlist := &cobra.Command{
		Use:          "list",
		Short:        "Show a workspace's queue",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if queueWS == 0 {
				return fmt.Errorf("which workspace? pass --workspace <id>")
			}
			var v client.QueueView
			if err := f.client().Do(cmd.Context(), http.MethodGet,
				"/api/v1/workspaces/"+strconv.FormatInt(queueWS, 10)+"/queue", nil, &v); err != nil {
				return err
			}
			fmt.Printf("%d running, %d waiting\n", v.Running, v.Queued)
			table("UNIT\tSTATE\tKIND\tRUN\tSINCE", func(w *tabwriter.Writer) {
				for _, e := range v.Entries {
					runID := "—"
					if e.Run != nil {
						runID = strconv.FormatInt(*e.Run, 10)
					}
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", e.Unit, e.State, e.Kind, runID, e.Since)
				}
			})
			return nil
		},
	}
	qlist.Flags().Int64Var(&queueWS, "workspace", 0, "the workspace to look at")
	queue.AddCommand(qlist)
	queue.AddCommand(&cobra.Command{
		Use:          "cancel <unit>",
		Short:        "Stop a queued or running unit — the work, not just the row",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.client().Do(cmd.Context(), http.MethodDelete, "/api/v1/queue/"+args[0], nil, nil); err != nil {
				return err
			}
			fmt.Println("stopped", args[0])
			return nil
		},
	})

	runs := &cobra.Command{
		Use:          "run <id>",
		Short:        "Read a delivery back from the ledger",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var r client.Run
			if err := f.client().Do(cmd.Context(), http.MethodGet, "/api/v1/inlet-runs/"+args[0], nil, &r); err != nil {
				return err
			}
			fmt.Printf("run %d  %s  %s/%s  agent %s\n", r.ID, r.State, r.InletAddress, r.TaskName, r.AgentName)
			if r.Error != "" {
				fmt.Println("\nerror:", r.Error)
			}
			if r.Result != "" {
				fmt.Println("\n" + r.Result)
			}
			if len(r.Did) > 0 {
				fmt.Println("\ndid:", string(r.Did))
			}
			if r.State != "completed" {
				os.Exit(1)
			}
			return nil
		},
	}

	for _, c := range []*cobra.Command{workspaces, gears, receivers, queue, runs} {
		f.bind(c)
	}
	return []*cobra.Command{login, workspaces, gears, receivers, queue, runs}
}

// flag01 writes a boolean the way the export route reads one. It refuses
// anything it does not recognise rather than treating it as off, so this sends
// only what it accepts.
func flag01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// gearID accepts a name or an id, because a person types the name and a script
// holds the id.
func gearID(cmd *cobra.Command, c *client.Client, ref string) (int64, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return id, nil
	}
	var all []client.Gear
	if err := c.Do(cmd.Context(), http.MethodGet, "/api/v1/gears", nil, &all); err != nil {
		return 0, err
	}
	for _, g := range all {
		if g.Name == ref {
			return g.ID, nil
		}
	}
	return 0, fmt.Errorf("no gear called %q in this install — `cogitorium gears list` shows them", ref)
}
