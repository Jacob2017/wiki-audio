package cli

import "github.com/spf13/cobra"

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Re-run the curl|bash installer",
		RunE:  notImplemented("upgrade"),
	}
}
