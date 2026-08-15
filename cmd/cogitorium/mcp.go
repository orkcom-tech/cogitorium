package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/mcp"
	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/spf13/cobra"
)

// newMCPCmd serves this install's gears and receivers over the Model Context
// Protocol, on stdin and stdout, for a client that spawns it.
//
// It is a client of the HTTP API rather than a second server: it opens no
// database and holds no state, so it can be started and killed by an MCP client
// as many times as that client likes without ever contending with the running
// Cogitorium for its SQLite file.
//
// That also means every credential is explicit. The management token decides
// which gears and receivers can be SEEN; a receiver's own key is what lets
// anything be DELIVERED to it, and there is no default for it — a door's key
// is the door's, and borrowing the admin's would put the wrong caller in the
// ledger.
func newMCPCmd() *cobra.Command {
	var (
		server    string
		token     string
		inletKeys []string
		wsID      int64
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve this install's gears and receivers over the Model Context Protocol (stdio)",
		Long: `Serve this install's tools to an MCP client such as Claude Desktop or Cursor.

Approved gears and receiver tasks become MCP tools. Nothing else is exposed:
creating agents, drawing wires and approving gears stay with the operator.

The server must already be running; this speaks to it over HTTP.

  cogitorium mcp --server http://127.0.0.1:8688 --token $COGITORIUM_TOKEN \
    --inlet-key tickets=cgi-tickets-…

In a client's configuration, that command line is the whole integration.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				token = os.Getenv("COGITORIUM_TOKEN")
			}
			if server == "" {
				server = os.Getenv("COGITORIUM_URL")
			}
			if server == "" {
				server = "http://127.0.0.1:8688"
			}

			keys := map[string]string{}
			for _, kv := range inletKeys {
				addr, key, ok := strings.Cut(kv, "=")
				if !ok || addr == "" || key == "" {
					return fmt.Errorf("--inlet-key wants address=key, got %q", kv)
				}
				keys[addr] = key
			}

			mcp.Version = version.Version
			backend := &mcp.HTTPBackend{
				BaseURL:     server,
				Token:       token,
				InletKeys:   keys,
				WorkspaceID: wsID,
				HTTP:        &http.Client{},
			}
			// stdout is the protocol. Anything else written there corrupts the
			// stream, which is why this command logs nothing: the server's own
			// log is where operational detail belongs, and a client that spawned
			// this process is not reading its stderr either.
			return mcp.NewServer(backend, os.Stdout).Serve(cmd.Context(), os.Stdin)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Cogitorium's address (default $COGITORIUM_URL, then http://127.0.0.1:8688)")
	cmd.Flags().StringVar(&token, "token", "", "management token, deciding what can be listed (default $COGITORIUM_TOKEN)")
	cmd.Flags().StringArrayVar(&inletKeys, "inlet-key", nil, "address=key for a receiver to offer; repeatable")
	cmd.Flags().Int64Var(&wsID, "workspace", 0, "offer receivers from this workspace only")
	return cmd
}
