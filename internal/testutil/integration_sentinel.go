package testutil

// IntegrationFileLoaded is true only when the package was built with
// -tags=integration. The default value (false) is overridden by
// integration_sentinel_integration.go when the build tag is on.
//
// gating_test.go uses this to prove that `go test ./...` (no tags) does
// NOT compile integration-tagged files into the unit run.
var IntegrationFileLoaded = false
