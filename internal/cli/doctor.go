package cli

import "github.com/spf13/cobra"

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Verify config + secrets + dependency reachability",
		RunE:  notImplemented("doctor"),
	}
}
