package tts

// SetAPIBaseURLForTest swaps the package-level apiBaseURL so tests
// in OTHER packages (e.g. internal/cli/build_pipeline_test.go) can
// point a real *Client at an httptest.NewServer URL.
//
// Returns a restore function the caller registers with t.Cleanup.
// Not for production use — the only legitimate caller is a _test.go
// file that pairs every call with the returned restore.
//
// Mirrors the package-private setAPIBaseURLForTest in client_test.go
// but is exported so cross-package tests don't have to redeclare the
// hook. The duplication is intentional: client_test.go's helper is
// internal to this package; this one is the public test-only surface.
func SetAPIBaseURLForTest(baseURL string) func() {
	prev := apiBaseURL
	apiBaseURL = baseURL
	return func() {
		apiBaseURL = prev
	}
}
