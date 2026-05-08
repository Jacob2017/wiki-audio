package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/joho/godotenv"
)

// RequiredEnvVars are the four secrets the wiki-audio CLI needs to
// run a build or publish (§2 + wa-kyn.3). WIKI_AUDIO_ACCESS_TOKEN is
// consumed by the feed generator to gate enclosure URLs; the other
// three are HTTP credentials. R2_TOKEN is informational and not
// required (some Cloudflare client paths use it; minio-go does not).
//
// Exported so wa-kyn.13 (doctor) can show the same list to the user
// without redeclaring it, keeping a single source of truth.
var RequiredEnvVars = []string{
	"ELEVENLABS_API_KEY",
	"R2_ACCESS_KEY_ID",
	"R2_SECRET_ACCESS_KEY",
	"WIKI_AUDIO_ACCESS_TOKEN",
}

// LoadEnv loads ~/.wiki-audio/.env (or whatever path the caller
// passes — root.go threads --env / --env-local through), enforces
// chmod 600 per §6, and validates that the four RequiredEnvVars are
// populated.
//
// Permission semantics: any group- or world-readable bit is fatal
// (mode & 0o077 != 0). chmod 0600 and 0400 pass. Windows skips the
// perm check — Go's os.FileMode doesn't model Unix permission bits
// portably there.
//
// godotenv.Load semantics: keys already present in the process env
// (from the shell, CI, etc.) are NOT overridden. This matches the
// usual convention that explicit shell env beats .env. Tests that
// need the .env values to actually populate must Unsetenv first.
//
// Missing .env: returns an error wrapping fs.ErrNotExist so callers
// that can tolerate a missing file (e.g. `wiki-audio init` when
// scaffolding) detect the case via errors.Is(err, fs.ErrNotExist).
//
// Never logs secrets. The slog.Debug at the bottom of the happy path
// records the count of required vars but no values. Don't add Secret
// fields to model.Config and slog them — secrets live in os.Getenv,
// retrieved at use-site only.
func LoadEnv(path string) error {
	if err := assertEnvPermissions(path); err != nil {
		return err
	}
	if err := godotenv.Load(path); err != nil {
		return fmt.Errorf("config: load .env at %s: %w", path, err)
	}
	if err := validateRequiredEnv(); err != nil {
		return err
	}
	slog.Debug("config: loaded .env",
		"path", path,
		"required_vars", len(RequiredEnvVars))
	return nil
}

// assertEnvPermissions stats the .env file and refuses to proceed if
// it's group- or world-readable. Cheap defense against API keys
// leaking on a shared box. The function returns the os.Stat error
// (wrapped) for missing files, so callers that tolerate absence can
// still use errors.Is(err, fs.ErrNotExist).
func assertEnvPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: .env not found at %s: %w", path, err)
		}
		return fmt.Errorf("config: stat .env at %s: %w", path, err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Errorf(
			"config: refusing to load .env: %s is readable by other users (mode %#o). "+
				"Run: chmod 600 %s",
			path, mode, path)
	}
	return nil
}

// validateRequiredEnv checks that every key in RequiredEnvVars is
// set to a non-empty string in the process env. Empty values are
// treated as missing — a key like ELEVENLABS_API_KEY="" is a
// half-configured .env, not a deliberate disable.
func validateRequiredEnv() error {
	var missing []string
	for _, name := range RequiredEnvVars {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"config: missing required env vars: %s. "+
			"Edit ~/.wiki-audio/.env or run `wiki-audio doctor` for diagnostics",
		strings.Join(missing, ", "))
}
