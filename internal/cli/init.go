package cli

import "github.com/spf13/cobra"

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold ~/.config/wiki-audio/",
		RunE:  notImplemented("init"),
	}
}
