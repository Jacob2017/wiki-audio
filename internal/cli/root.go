package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type rootFlags struct {
	configPath string
	envPath    string
	envLocal   bool
	verbose    bool
	quiet      bool
	json       bool
	version    bool
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".wiki-audio", "config.toml")
}

func defaultEnvPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".env"
	}
	return filepath.Join(home, ".wiki-audio", ".env")
}

// NewRootCmd builds the wiki-audio Cobra command tree. All subcommands
// are registered up-front (§3, wa-jlb.5) so `wiki-audio --help` shows
// the full command surface even before later phases fill in bodies.
func NewRootCmd() *cobra.Command {
	flags := &rootFlags{}

	root := &cobra.Command{
		Use:           "wiki-audio",
		Short:         "PG essays → personal podcast feed",
		Long:          "wiki-audio extracts, synthesizes, and publishes PG essays as a private podcast feed. See https://github.com/Jacob2017/wiki-audio.",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if flags.envLocal {
				flags.envPath = ".env"
			}
			slog.SetDefault(newLogger(cmd.ErrOrStderr(), flags))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.version {
				fmt.Fprintln(cmd.OutOrStdout(), VersionLine())
				return nil
			}
			return cmd.Help()
		},
	}

	root.PersistentFlags().StringVar(&flags.configPath, "config", defaultConfigPath(), "path to config.toml")
	root.PersistentFlags().StringVar(&flags.envPath, "env", defaultEnvPath(), "path to .env")
	root.PersistentFlags().BoolVar(&flags.envLocal, "env-local", false, "load .env from current working directory (mutually exclusive with --env)")
	root.PersistentFlags().BoolVarP(&flags.verbose, "verbose", "v", false, "DEBUG slog level")
	root.PersistentFlags().BoolVarP(&flags.quiet, "quiet", "q", false, "WARN slog level")
	root.PersistentFlags().BoolVar(&flags.json, "json", false, "emit logs as JSON (slog.JSONHandler)")
	root.Flags().BoolVar(&flags.version, "version", false, "print version and exit")

	root.MarkFlagsMutuallyExclusive("verbose", "quiet")
	root.MarkFlagsMutuallyExclusive("env", "env-local")

	root.AddCommand(
		newInitCmd(),
		newDoctorCmd(),
		newBuildCmd(),
		newPublishCmd(),
		newInspectCmd(),
		newCostCmd(),
		newUpgradeCmd(),
	)

	return root
}

func newLogger(w io.Writer, f *rootFlags) *slog.Logger {
	level := slog.LevelInfo
	switch {
	case f.verbose:
		level = slog.LevelDebug
	case f.quiet:
		level = slog.LevelWarn
	}
	opts := &slog.HandlerOptions{Level: level}
	if f.json {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func notImplemented(name string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("%s: not yet implemented", name)
	}
}
