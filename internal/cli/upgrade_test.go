package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestUpgrade_CheckWritableHappyPath — t.TempDir is writable to the test
// process, so checkWritable must return nil.
func TestUpgrade_CheckWritableHappyPath(t *testing.T) {
	if err := checkWritable(t.TempDir()); err != nil {
		t.Errorf("checkWritable on a fresh TempDir should pass: %v", err)
	}
}

// TestUpgrade_CheckWritableRejectsReadOnly — chmod 0500 makes the dir
// non-writable for the owner; checkWritable must return a non-nil error.
func TestUpgrade_CheckWritableRejectsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		// Windows mode bits don't gate write the same way, and root
		// bypasses unix permission checks entirely.
		t.Skip("permission probing requires non-root on Unix")
	}
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "ro")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	if err := checkWritable(readOnly); err == nil {
		t.Errorf("checkWritable on a 0500 dir should fail")
	}
}

// TestUpgrade_CurrentBinaryPath — the test binary is a real executable;
// currentBinaryPath must succeed and return a path that os.Stat-s.
func TestUpgrade_CurrentBinaryPath(t *testing.T) {
	got, err := currentBinaryPath()
	if err != nil {
		t.Fatalf("currentBinaryPath: %v", err)
	}
	if got == "" {
		t.Fatal("currentBinaryPath returned empty string")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("returned path does not stat: %v", err)
	}
	if info.IsDir() {
		t.Errorf("currentBinaryPath returned a directory: %s", got)
	}
}

// TestUpgrade_PipeInstall_RunsRemoteScript — spins up an httptest server
// that serves a minimal install.sh stub. The stub writes a marker file
// into INSTALL_DIR, asserting both that (a) the curl|bash pipeline plumbs
// stdout-of-curl into stdin-of-bash, and (b) INSTALL_DIR env passthrough
// reaches the script.
func TestUpgrade_PipeInstall_RunsRemoteScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash + curl + Unix-y file paths")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}

	// Stub install.sh: prints a marker, writes a marker file at
	// $INSTALL_DIR/wiki-audio. We deliberately use plain shell so the
	// test is hermetic — no goreleaser invocation, no real download.
	const stubScript = `#!/bin/sh
echo "stub install starting"
echo "INSTALL_DIR=$INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
printf 'stub-binary-content\n' > "$INSTALL_DIR/wiki-audio"
chmod +x "$INSTALL_DIR/wiki-audio"
echo "stub install done"
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript")
		_, _ = w.Write([]byte(stubScript))
	}))
	t.Cleanup(srv.Close)

	installDir := t.TempDir()
	var stdout, stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := pipeInstall(ctx, srv.URL+"/install.sh", installDir, &stdout, &stderr); err != nil {
		t.Fatalf("pipeInstall: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "stub install starting") {
		t.Errorf("stub install.sh output missing from stdout; got:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "INSTALL_DIR="+installDir) {
		t.Errorf("INSTALL_DIR env did not pass through to the script; stdout:\n%s", stdout.String())
	}

	marker := filepath.Join(installDir, "wiki-audio")
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker file %q not produced by stub: %v", marker, err)
	}
	if !strings.Contains(string(body), "stub-binary-content") {
		t.Errorf("marker file content unexpected: %q", string(body))
	}
}

// TestUpgrade_PipeInstall_BashNonzeroSurfaces — when install.sh exits
// non-zero, pipeInstall must surface that as an error containing the
// exit code (or wrapped non-nil err) rather than silently succeeding.
func TestUpgrade_PipeInstall_BashNonzeroSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash + curl")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}

	const failScript = `#!/bin/sh
echo "stub failing on purpose" >&2
exit 7
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(failScript))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := pipeInstall(ctx, srv.URL+"/install.sh", t.TempDir(), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when install.sh exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), "bash") {
		t.Errorf("error should mention bash exit; got: %v", err)
	}
}

// TestUpgrade_PipeInstall_HTTPErrorSurfaces — a 404 from the install.sh
// URL must surface as an error mentioning curl. Without the -fsSL `f`
// flag, curl swallows HTTP errors silently — this test is the regression
// guard for that flag dropping out.
func TestUpgrade_PipeInstall_HTTPErrorSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires curl")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := pipeInstall(ctx, srv.URL+"/install.sh", t.TempDir(), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "curl") {
		t.Errorf("error should mention curl; got: %v", err)
	}
}

// TestUpgrade_NewUpgradeCmd_HelpMentionsURL — surface check that the
// --url flag is registered and visible. Cobra silently dropping a flag
// is not a normal failure mode but a help diff is the cheapest pin.
func TestUpgrade_NewUpgradeCmd_HelpMentionsURL(t *testing.T) {
	cmd := newUpgradeCmd()
	if cmd.Use != "upgrade" {
		t.Errorf("Use = %q, want %q", cmd.Use, "upgrade")
	}
	if cmd.Flags().Lookup("url") == nil {
		t.Errorf("--url flag should be registered")
	}
}
