package testutil

import (
	"strings"
	"testing"
)

// TestRequireIntegration_SkipsWithoutEnv runs RequireIntegration inside a
// subtest with the env unset; code following the call must NOT execute.
func TestRequireIntegration_SkipsWithoutEnv(t *testing.T) {
	t.Setenv(IntegrationEnv, "")

	var ranAfterCall bool
	t.Run("gated", func(t *testing.T) {
		RequireIntegration(t)
		ranAfterCall = true
	})

	if ranAfterCall {
		t.Errorf("code after RequireIntegration executed with env unset; should have skipped")
	}
}

// TestRequireIntegration_RunsWithEnv confirms that setting the env to "1"
// allows the test body to run.
func TestRequireIntegration_RunsWithEnv(t *testing.T) {
	t.Setenv(IntegrationEnv, "1")

	var ranAfterCall bool
	t.Run("gated", func(t *testing.T) {
		RequireIntegration(t)
		ranAfterCall = true
	})

	if !ranAfterCall {
		t.Errorf("RequireIntegration skipped despite %s=1", IntegrationEnv)
	}
}

// TestSkipMessage_NamesEnvVar locks in the contract that the skip message
// names the env var to set. A future refactor that removes this name from
// the message should fail loudly here.
func TestSkipMessage_NamesEnvVar(t *testing.T) {
	t.Setenv(IntegrationEnv, "")

	run, msg := integrationGated()
	if run {
		t.Fatalf("gate open with %s unset", IntegrationEnv)
	}
	if !strings.Contains(msg, IntegrationEnv) {
		t.Errorf("skip message should name %q so a developer knows what to set; got %q", IntegrationEnv, msg)
	}
}

// TestRequireCredentials_SkipsWhenMissing exercises the missing-vars path
// and confirms the skip message names the missing var(s).
func TestRequireCredentials_SkipsWhenMissing(t *testing.T) {
	t.Setenv("FAKE_REQUIRED_VAR", "")

	missing := missingCredentials([]string{"FAKE_REQUIRED_VAR"})
	if len(missing) != 1 || missing[0] != "FAKE_REQUIRED_VAR" {
		t.Errorf("missingCredentials should report unset var; got %v", missing)
	}

	var ranAfterCall bool
	t.Run("gated", func(t *testing.T) {
		RequireCredentials(t, "FAKE_REQUIRED_VAR")
		ranAfterCall = true
	})

	if ranAfterCall {
		t.Errorf("code after RequireCredentials executed despite missing var; should have skipped")
	}
}

// TestRequireCredentials_RunsWhenAllSet confirms that the gate opens when
// every named var has a non-empty value.
func TestRequireCredentials_RunsWhenAllSet(t *testing.T) {
	t.Setenv("FAKE_REQUIRED_VAR_A", "x")
	t.Setenv("FAKE_REQUIRED_VAR_B", "y")

	if missing := missingCredentials([]string{"FAKE_REQUIRED_VAR_A", "FAKE_REQUIRED_VAR_B"}); len(missing) != 0 {
		t.Fatalf("missingCredentials reported %v despite both vars being set", missing)
	}

	var ranAfterCall bool
	t.Run("gated", func(t *testing.T) {
		RequireCredentials(t, "FAKE_REQUIRED_VAR_A", "FAKE_REQUIRED_VAR_B")
		ranAfterCall = true
	})

	if !ranAfterCall {
		t.Errorf("RequireCredentials skipped despite both vars set")
	}
}
