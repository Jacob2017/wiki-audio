package cli

import "github.com/spf13/cobra"

func newInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect",
		Short: "Read-only diagnostics for one essay",
		RunE:  notImplemented("inspect"),
	}
}
