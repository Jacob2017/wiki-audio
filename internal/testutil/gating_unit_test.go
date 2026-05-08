//go:build !integration

package testutil

import "testing"

// TestUnitRun_DoesNotIncludeIntegrationFiles is the structural check that
// `go test ./...` (no -tags=integration) does NOT compile integration-tagged
// files into the run. The sentinel flag is flipped to true only by an init()
// in integration_sentinel_integration.go, which carries the integration tag.
//
// This test itself carries the inverse tag (`//go:build !integration`) so it
// is excluded when -tags=integration is on — under that mode the sentinel
// flag IS expected to be true, and asserting otherwise would be a false
// regression.
func TestUnitRun_DoesNotIncludeIntegrationFiles(t *testing.T) {
	if IntegrationFileLoaded {
		t.Errorf("IntegrationFileLoaded == true under unit run; the integration build tag is leaking into go test")
	}
}
