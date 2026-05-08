package cli

import "github.com/spf13/cobra"

func newPublishCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "publish",
		Short: "Diff + upload MP3s + regenerate RSS feed",
		RunE:  notImplemented("publish"),
	}
}
