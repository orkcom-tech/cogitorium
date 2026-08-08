package main

import (
	"fmt"
	"os"

	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "cogitorium",
		Short:         "Cogitorium — a workbench for agentic development",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newServeCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print cogitorium version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("cogitorium " + version.Version)
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
