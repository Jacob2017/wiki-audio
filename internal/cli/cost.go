package cli

import "github.com/spf13/cobra"

func newCostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cost",
		Short: "Estimate ElevenLabs credit cost",
		RunE:  notImplemented("cost"),
	}
}
