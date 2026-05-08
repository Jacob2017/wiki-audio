package cli

import "github.com/spf13/cobra"

func newBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Extract + synthesize stale essays",
		RunE:  notImplemented("build"),
	}
}
