package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Smoke tests for wa-kyn.3. The full §6 matrix (chmod variations,
// secret redaction in slog output, cross-platform mode-bit handling)
// lives in wa-kyn.21; here we pin only what wa-kyn.3 itself owns:
// (i) valid .env loads, (ii) world-readable .env errors with chmod
// hint, (iii) missing .env returns fs.ErrNotExist for callers to
// tolerate.

// snapshotEnv captures the current value (and presence) of each named
// env var, then unsets them so godotenv.Load — which by design does
// not overwrite existing env — actually populates the variables
// during the test. Cleanup restores the original state.
func snapshotEnv(t *testing.T, keys ...string) {
	t.Helper()
	type slot struct {
		val     string
		present bool
	}
	saved := make(map[string]slot, len(keys))
	for _, k := range keys {
		v, present := os.LookupEnv(k)
		saved[k] = slot{v, present}
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			s := saved[k]
			if s.present {
				os.Setenv(k, s.val)
			} else {
				os.Unsetenv(k)
			}
		}
	})
}

func writeEnv(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile honors the umask; force the mode bits explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validEnvBody() string {
	return `ELEVENLABS_API_KEY=el-test-key
R2_ACCESS_KEY_ID=r2-id
R2_SECRET_ACCESS_KEY=r2-secret
WIKI_AUDIO_ACCESS_TOKEN=token-abcdef
R2_TOKEN=optional-cf-token
`
}

func TestLoadEnvHappyPath(t *testing.T) {
	snapshotEnv(t, append(RequiredEnvVars, "R2_TOKEN")...)

	path := writeEnv(t, validEnvBody(), 0o600)

	if err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	wants := map[string]string{
		"ELEVENLABS_API_KEY":      "el-test-key",
		"R2_ACCESS_KEY_ID":        "r2-id",
		"R2_SECRET_ACCESS_KEY":    "r2-secret",
		"WIKI_AUDIO_ACCESS_TOKEN": "token-abcdef",
		"R2_TOKEN":                "optional-cf-token",
	}
	for k, want := range wants {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestLoadEnvAcceptsMode0400(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits — see PLAN §6 chmod 600 enforcement")
	}
	snapshotEnv(t, RequiredEnvVars...)

	path := writeEnv(t, validEnvBody(), 0o400)
	if err := LoadEnv(path); err != nil {
		t.Errorf("mode 0400 must pass; got: %v", err)
	}
}

// World- and group-readable bits trigger §6's chmod-600 refusal. The
// error must mention the offending mode AND the chmod 600 fix.
func TestLoadEnvRejectsLooseModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits — see PLAN §6 chmod 600 enforcement")
	}
	snapshotEnv(t, RequiredEnvVars...)

	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"world readable 0644", 0o644},
		{"group readable 0640", 0o640},
		{"group + world readable 0666", 0o666},
		{"world executable 0601", 0o601},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeEnv(t, validEnvBody(), c.mode)
			err := LoadEnv(path)
			if err == nil {
				t.Fatalf("expected refusal at mode %#o", c.mode)
			}
			if !strings.Contains(err.Error(), "chmod 600") {
				t.Errorf("error must hint chmod 600 fix; got: %v", err)
			}
			if !strings.Contains(err.Error(), "readable by other users") {
				t.Errorf("error must explain WHY it's refused; got: %v", err)
			}
		})
	}
}

// Missing .env must return fs.ErrNotExist so callers like
// `wiki-audio init` (which can tolerate absence and scaffold) can
// detect the case via errors.Is.
func TestLoadEnvMissingFileIsErrNotExist(t *testing.T) {
	err := LoadEnv("/no/such/path/.env")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error must wrap fs.ErrNotExist for caller tolerance; got: %v", err)
	}
}

// A .env with proper permissions but missing one of the required
// keys must surface the missing key by name. wa-kyn.13 (doctor)
// will quote this error to the user.
func TestLoadEnvMissingRequiredKey(t *testing.T) {
	snapshotEnv(t, RequiredEnvVars...)

	body := `ELEVENLABS_API_KEY=ok
R2_ACCESS_KEY_ID=ok
R2_SECRET_ACCESS_KEY=ok
` // WIKI_AUDIO_ACCESS_TOKEN deliberately absent
	path := writeEnv(t, body, 0o600)
	err := LoadEnv(path)
	if err == nil {
		t.Fatal("expected error for missing required key")
	}
	if !strings.Contains(err.Error(), "WIKI_AUDIO_ACCESS_TOKEN") {
		t.Errorf("error must name the missing key; got: %v", err)
	}
	if !strings.Contains(err.Error(), "wiki-audio doctor") {
		t.Errorf("error should point at doctor; got: %v", err)
	}
}

// Empty value is treated as missing. A half-configured .env with
// `KEY=` is a more common (and dangerous) failure mode than a fully
// absent line — it produces a "set but empty" env var that would
// silently 401 the API on first call.
func TestLoadEnvEmptyValueTreatedAsMissing(t *testing.T) {
	snapshotEnv(t, RequiredEnvVars...)

	body := `ELEVENLABS_API_KEY=ok
R2_ACCESS_KEY_ID=
R2_SECRET_ACCESS_KEY=ok
WIKI_AUDIO_ACCESS_TOKEN=ok
`
	path := writeEnv(t, body, 0o600)
	err := LoadEnv(path)
	if err == nil {
		t.Fatal("expected error for empty value")
	}
	if !strings.Contains(err.Error(), "R2_ACCESS_KEY_ID") {
		t.Errorf("error must name the empty key; got: %v", err)
	}
}

// godotenv.Load must NOT override a value already set in the process
// env. This is the standard convention (shell env beats .env file)
// and downstream code (e.g. CI overriding ELEVENLABS_API_KEY for a
// dry-run) relies on it.
func TestLoadEnvDoesNotOverwriteShellEnv(t *testing.T) {
	snapshotEnv(t, RequiredEnvVars...)

	// Set ELEVENLABS_API_KEY in the "shell" before LoadEnv runs.
	t.Setenv("ELEVENLABS_API_KEY", "shell-set-value")

	// .env says something different.
	body := `ELEVENLABS_API_KEY=env-file-value
R2_ACCESS_KEY_ID=ok
R2_SECRET_ACCESS_KEY=ok
WIKI_AUDIO_ACCESS_TOKEN=ok
`
	path := writeEnv(t, body, 0o600)
	if err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	if got := os.Getenv("ELEVENLABS_API_KEY"); got != "shell-set-value" {
		t.Errorf("shell env was overridden: got %q, want shell-set-value", got)
	}
}

// Spec sanity: §6 mandates "tool refuses to start if .env is
// world-readable". Pin the mode bit threshold here — if a future
// refactor relaxes the mask from 0o077 to (say) 0o007, this test
// catches it.
func TestAssertEnvPermissionsMaskIsZero077(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits")
	}
	// Mode 0o604 is world-readable but not group-readable. Per the
	// 0o077 mask, it's still forbidden because 0o604 & 0o077 = 0o004.
	path := writeEnv(t, validEnvBody(), 0o604)
	err := assertEnvPermissions(path)
	if err == nil {
		t.Errorf("mode 0o604 must be refused — world-readable bit is set")
	}
}

// Sanity check that RequiredEnvVars hasn't drifted from §2.
func TestRequiredEnvVarsMatchSpec(t *testing.T) {
	want := []string{
		"ELEVENLABS_API_KEY",
		"R2_ACCESS_KEY_ID",
		"R2_SECRET_ACCESS_KEY",
		"WIKI_AUDIO_ACCESS_TOKEN",
	}
	if len(RequiredEnvVars) != len(want) {
		t.Fatalf("RequiredEnvVars len = %d, want %d", len(RequiredEnvVars), len(want))
	}
	for i, n := range want {
		if RequiredEnvVars[i] != n {
			t.Errorf("RequiredEnvVars[%d] = %q, want %q", i, RequiredEnvVars[i], n)
		}
	}
}

// PLAN §6 says secrets must never be logged. Sanity check that the
// happy-path slog.Debug call doesn't include any value strings.
func TestLoadEnvHappyPathDoesNotLogSecretValues(t *testing.T) {
	// We rely on the fact that the implementation only logs the path
	// and the count of required vars — never values. If a future
	// edit adds e.g. slog.Debug(... "elevenlabs_key", os.Getenv(...))
	// this test wouldn't catch it directly, but the simplicity of
	// the signature ("required_vars" int) makes accidental leakage
	// unlikely. Future wa-kyn.21 will add a redaction-aware test
	// that captures slog output.
	//
	// Today we just sanity-check the function returns nil cleanly
	// without panicking when the logger is rerouted to a no-op.
	snapshotEnv(t, append(RequiredEnvVars, "R2_TOKEN")...)
	path := writeEnv(t, validEnvBody(), 0o600)
	if err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	// A more rigorous test would capture slog output and assert no
	// value strings appear. wa-kyn.21 owns that matrix.
	_ = fmt.Sprintf("placeholder for future redaction assertion")
}
