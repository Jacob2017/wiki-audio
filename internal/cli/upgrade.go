package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// DefaultInstallURL is the canonical install.sh shipped on the main branch.
// Override with --url for testing or to pin to a tagged version of the script.
const DefaultInstallURL = "https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh"

// installURLEnv lets tests redirect the upgrade pipeline to a localhost
// install.sh without changing the public --url flag default. It also gives
// air-gapped users a way to script a private mirror.
const installURLEnv = "WIKI_AUDIO_INSTALL_URL"

type upgradeFlags struct {
	url string
}

func newUpgradeCmd() *cobra.Command {
	flags := &upgradeFlags{}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Re-run the install.sh installer (overwrites the running binary in-place)",
		Long: `Fetch the latest install.sh and pipe it to bash, instructing it to
install into the same directory the running wiki-audio binary lives in.

Implementation: shells out to bash with INSTALL_DIR set, exactly the
flow documented in install.sh. We use the script as the single source
of truth for "how to install" rather than reimplementing fetch +
sha256-verify + extract in Go (wa-8gt.5).

Self-overwrite is safe on Linux/macOS: the kernel keeps the
already-mapped pages of the running binary, so wiki-audio can replace
its own file on disk; the next invocation runs the new binary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), flags)
		},
	}
	defaultURL := DefaultInstallURL
	if env := os.Getenv(installURLEnv); env != "" {
		defaultURL = env
	}
	cmd.Flags().StringVar(&flags.url, "url", defaultURL,
		"install.sh URL to fetch and pipe to bash (override via "+installURLEnv+")")
	return cmd
}

// runUpgrade is split out from newUpgradeCmd so tests can drive it without
// constructing a Cobra command tree.
func runUpgrade(ctx context.Context, stdout, stderr io.Writer, flags *upgradeFlags) error {
	exePath, err := currentBinaryPath()
	if err != nil {
		return err
	}
	installDir := filepath.Dir(exePath)

	fmt.Fprintf(stdout, "detected current binary: %s\n", exePath)
	fmt.Fprintf(stdout, "current version:        %s\n", Version)

	if err := checkWritable(installDir); err != nil {
		return fmt.Errorf("%s is not writable; rerun with sudo or move the binary: %w", installDir, err)
	}

	fmt.Fprintf(stdout, "fetching install.sh:    %s\n", flags.url)
	fmt.Fprintf(stdout, "running install.sh with INSTALL_DIR=%s\n", installDir)

	if err := pipeInstall(ctx, flags.url, installDir, stdout, stderr); err != nil {
		return fmt.Errorf("install.sh failed: %w", err)
	}

	// install.sh has overwritten exePath; ask the new binary for its
	// version. Best-effort: a missing or hung post-install --version
	// shouldn't make the upgrade itself look failed, since install.sh
	// has already verified the sha256 and dropped the binary into place.
	newVersion, err := capturedVersion(ctx, exePath)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "upgrade complete. (was %s → could not read new version: %v)\n", Version, err)
	case newVersion == Version:
		fmt.Fprintf(stdout, "upgrade complete. (already at %s; no version change)\n", Version)
	default:
		fmt.Fprintf(stdout, "upgrade complete. (was %s → now %s)\n", Version, newVersion)
	}
	return nil
}

// currentBinaryPath returns the absolute, symlink-resolved path of the
// running binary. EvalSymlinks failure is non-fatal — we keep the original
// path; the upgrade still works, it just installs into the directory of
// the symlink rather than its target.
func currentBinaryPath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not determine current binary path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	return exePath, nil
}

// checkWritable probes the install dir by creating and removing a temp
// file. Direct probe over a stat-and-mode-bits check, because real-world
// permissions depend on uid/gid + ACLs + filesystem flags, not just mode.
func checkWritable(dir string) error {
	tmp, err := os.CreateTemp(dir, ".wiki-audio-upgrade-probe-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return nil
}

// pipeInstall runs `curl -fsSL <url> | INSTALL_DIR=<dir> bash` and lets
// install.sh's stdout/stderr pass through unchanged. Errors from either
// side are wrapped with the URL and a stderr tail.
func pipeInstall(ctx context.Context, url, installDir string, stdout, stderr io.Writer) error {
	curl := exec.CommandContext(ctx, "curl", "-fsSL", url)
	bash := exec.CommandContext(ctx, "bash")
	bash.Env = append(os.Environ(), "INSTALL_DIR="+installDir)
	bash.Stdout = stdout
	bash.Stderr = stderr

	pipe, err := curl.StdoutPipe()
	if err != nil {
		return fmt.Errorf("curl pipe: %w", err)
	}
	bash.Stdin = pipe

	var curlErr strings.Builder
	curl.Stderr = &curlErr

	if err := bash.Start(); err != nil {
		return fmt.Errorf("bash start: %w", err)
	}
	curlErrFromRun := curl.Run()
	bashErr := bash.Wait()

	if curlErrFromRun != nil {
		return fmt.Errorf("curl %s: %w (curl stderr: %s)",
			url, curlErrFromRun, strings.TrimSpace(curlErr.String()))
	}
	if bashErr != nil {
		var exitErr *exec.ExitError
		if errors.As(bashErr, &exitErr) {
			return fmt.Errorf("bash exited %d", exitErr.ExitCode())
		}
		return fmt.Errorf("bash: %w", bashErr)
	}
	return nil
}

// capturedVersion runs the freshly-installed binary with --version and
// returns the trimmed stdout. The new binary doesn't need to be on PATH —
// we invoke it by absolute path.
func capturedVersion(ctx context.Context, exePath string) (string, error) {
	cmd := exec.CommandContext(ctx, exePath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
