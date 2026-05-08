// Package testutil provides shared helpers for the wiki-audio test suite.
//
// Integration test convention:
//
//	Tests that hit a real external service (ElevenLabs, R2, the Worker URL)
//	MUST live in files that begin with the build tag:
//
//	    //go:build integration
//
//	The first test in each integration file (or its TestMain) MUST call
//	RequireIntegration(t) so the env gate stays defense-in-depth alongside
//	the build tag.
//
// Run modes:
//
//	go test ./...                                                  // unit only
//	go test -tags=integration ./...                                // tag-on, env-gated skip
//	WIKI_AUDIO_RUN_INTEGRATION=1 go test -tags=integration ./...   // actually runs
//
// CI runs only `go test ./...` — never -tags=integration. Integration tests
// burn ~$0.01-0.50 per run depending on which service. Run them manually.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// IntegrationEnv is the env var that opts a test run into integration tests.
// Both the build tag AND this env var must be set for a gated test to run.
const IntegrationEnv = "WIKI_AUDIO_RUN_INTEGRATION"

// integrationGated returns whether integration tests are enabled and the
// skip message to use when not. Pulled out of RequireIntegration so the
// gating logic can be unit-tested without the testing.T side effects.
func integrationGated() (run bool, skipMsg string) {
	if os.Getenv(IntegrationEnv) == "1" {
		return true, ""
	}
	return false, fmt.Sprintf("set %s=1 to run integration tests", IntegrationEnv)
}

// RequireIntegration skips the calling test unless WIKI_AUDIO_RUN_INTEGRATION=1.
// Place it as the first call in any test compiled under -tags=integration.
func RequireIntegration(t *testing.T) {
	t.Helper()
	if run, msg := integrationGated(); !run {
		t.Skip(msg)
	}
}

// missingCredentials returns the names of any env vars in `vars` that are
// unset or empty. Pulled out for the same testability reason as integrationGated.
func missingCredentials(vars []string) []string {
	var missing []string
	for _, v := range vars {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}
	return missing
}

// RequireCredentials skips the calling test if any named env var is unset
// or empty. Naming the missing var in the skip message is more useful than
// "missing credentials" because the developer can see exactly which one to set.
func RequireCredentials(t *testing.T, vars ...string) {
	t.Helper()
	if missing := missingCredentials(vars); len(missing) > 0 {
		t.Skipf("integration test requires env: %s", strings.Join(missing, ", "))
	}
}

// RequireBinary skips the calling test if a named external binary is not
// resolvable on PATH. Use it for tests that shell out to ffmpeg, ffprobe,
// or similar. Naming the missing binary in the skip message means a CI
// runner that lacks ffmpeg gets "ffmpeg not on PATH", not silent green.
func RequireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("test requires %q on PATH: %v", name, err)
	}
}
