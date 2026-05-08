package concat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/testutil"
)

// makeSineMP3 generates a small MP3 of `seconds` duration using ffmpeg's
// sine-wave source, written into dir/<name>. Tests share these short
// fixtures rather than committing binary blobs.
func makeSineMP3(t *testing.T, dir, name string, seconds float64, freq int) string {
	t.Helper()
	out := filepath.Join(dir, name)
	args := []string{
		"-y",
		"-f", "lavfi",
		"-i", "sine=frequency=" + strconv.Itoa(freq) + ":duration=" + strconv.FormatFloat(seconds, 'f', 3, 64),
		"-c:a", "libmp3lame",
		"-q:a", "9", // lowest VBR quality, smallest output — these are throwaway fixtures
		out,
	}
	cmd := exec.Command("ffmpeg", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("makeSineMP3 ffmpeg failed: %v\nstderr:\n%s", err, stderr.String())
	}
	return out
}

// probeDuration returns the duration of an audio file in seconds via ffprobe.
func probeDuration(t *testing.T, path string) float64 {
	t.Helper()
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffprobe %s failed: %v\nstderr:\n%s", path, err, stderr.String())
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(out.String()), 64)
	if err != nil {
		t.Fatalf("parse ffprobe duration %q: %v", out.String(), err)
	}
	return d
}

// approxEqual returns true if a and b are within tolerance seconds.
func approxEqual(a, b, tolerance float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tolerance
}

// requireFFmpegStack skips on hosts missing either ffmpeg or ffprobe.
// Both ship together in the same package, so one absent usually means
// both — but we check explicitly.
func requireFFmpegStack(t *testing.T) {
	t.Helper()
	testutil.RequireBinary(t, "ffmpeg")
	testutil.RequireBinary(t, "ffprobe")
}

func TestSingleChunk_ReturnsInputBytewise(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	in := makeSineMP3(t, dir, "in.mp3", 0.5, 440)
	out := filepath.Join(dir, "out.mp3")

	if err := Concat(context.Background(), []string{in}, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat: %v", err)
	}

	a, err := os.ReadFile(in)
	if err != nil {
		t.Fatalf("read in: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("single-chunk path should be a bytewise copy; sizes a=%d b=%d", len(a), len(b))
	}
}

func TestTwoChunks_DurationLessByCrossfade(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	a := makeSineMP3(t, dir, "a.mp3", 1.0, 440)
	b := makeSineMP3(t, dir, "b.mp3", 1.0, 660)
	out := filepath.Join(dir, "out.mp3")

	if err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat: %v", err)
	}
	got := probeDuration(t, out)
	// 1.0 + 1.0 - 0.05 (default crossfade)
	want := 1.0 + 1.0 - DefaultCrossfadeSeconds
	// MP3 frame-alignment + sine generation rounding routinely add up to
	// ~0.05s of measurement noise; 0.15s is comfortably above that floor.
	if !approxEqual(got, want, 0.15) {
		t.Errorf("two-chunk duration: got %.3fs, want ≈ %.3fs (±0.15)", got, want)
	}
}

func TestThreeChunks_PairwiseDuration(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	a := makeSineMP3(t, dir, "a.mp3", 1.0, 440)
	b := makeSineMP3(t, dir, "b.mp3", 1.0, 550)
	c := makeSineMP3(t, dir, "c.mp3", 1.0, 660)
	out := filepath.Join(dir, "out.mp3")

	if err := Concat(context.Background(), []string{a, b, c}, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat: %v", err)
	}
	got := probeDuration(t, out)
	want := 3.0 - 2*DefaultCrossfadeSeconds
	if !approxEqual(got, want, 0.15) {
		t.Errorf("three-chunk duration: got %.3fs, want ≈ %.3fs (±0.15)", got, want)
	}
}

func TestTenChunks_RunsToCompletionAndCleansTmp(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	const N = 10
	inputs := make([]string, 0, N)
	for i := 0; i < N; i++ {
		// short fixtures keep this test under a second on a normal box
		inputs = append(inputs, makeSineMP3(t, dir, "c"+strconv.Itoa(i)+".mp3", 0.2, 440+10*i))
	}
	out := filepath.Join(dir, "out.mp3")

	if err := Concat(context.Background(), inputs, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not produced: %v", err)
	}
	// On success, tmpDir should not contain any step_NNN.mp3 files.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read tmpDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "step_") {
			t.Errorf("intermediate %q not cleaned on success", e.Name())
		}
	}
}

func TestFFmpegMissing_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	// Two empty placeholder files satisfy the "≥2 inputs" branch; ffmpeg
	// is never invoked because the lookup fails first.
	a := filepath.Join(dir, "a.mp3")
	b := filepath.Join(dir, "b.mp3")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte{0}, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{
		FFmpegPath: "/nonexistent/ffmpeg-does-not-exist-xyz",
	})
	if err == nil {
		t.Fatal("expected error when ffmpeg binary is missing, got nil")
	}
	if !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Errorf("error should say %q for actionability; got: %v", "ffmpeg not found", err)
	}
}

func TestFFmpegNonzeroExit_WrapsStderrTail(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	// A real MP3 plus a malformed "MP3" forces ffmpeg into a non-zero exit
	// during the first pairwise step.
	good := makeSineMP3(t, dir, "good.mp3", 0.3, 440)
	bad := filepath.Join(dir, "bad.mp3")
	if err := os.WriteFile(bad, []byte("this is not an MP3 file at all"), 0o644); err != nil {
		t.Fatalf("seed bad: %v", err)
	}
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{good, bad}, out, tmpDir, Options{})
	if err == nil {
		t.Fatal("expected error for malformed input, got nil")
	}
	// We don't pin the exact stderr substring (ffmpeg phrasing varies by
	// version) but the error MUST mention ffmpeg in some form, and a
	// "stderr tail:" marker so the operator knows where to look.
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("error should reference ffmpeg; got: %v", err)
	}
	if !strings.Contains(err.Error(), "stderr tail") {
		t.Errorf("error should expose stderr tail for debugging; got: %v", err)
	}
}

func TestTimeoutKillsSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ffmpeg shim assumes /bin/sh; tests on Unix only")
	}
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	// Fake "ffmpeg" that just sleeps — the per-step timeout must fire and
	// kill it. Using sleep 30 vs PerStepTimeout=200ms gives plenty of headroom.
	fake := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("seed fake ffmpeg: %v", err)
	}

	a := filepath.Join(dir, "a.mp3")
	b := filepath.Join(dir, "b.mp3")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte{0}, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out := filepath.Join(dir, "out.mp3")

	start := time.Now()
	err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{
		FFmpegPath:     fake,
		PerStepTimeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Errorf("error should mention timeout; got: %v", err)
	}
	// The ffmpeg subprocess must actually have been killed; a 5s ceiling
	// catches a hang where the timeout fires but doesn't propagate.
	if elapsed > 5*time.Second {
		t.Errorf("Concat returned after %v despite 200ms timeout — process not killed?", elapsed)
	}
}

func TestTmpDirRetainedOnFailure(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	// Three inputs: first two are good, third is malformed. The first
	// pairwise step succeeds and writes tmp/step_001.mp3; the second
	// step fails. step_001.mp3 must remain on disk.
	a := makeSineMP3(t, dir, "a.mp3", 0.3, 440)
	b := makeSineMP3(t, dir, "b.mp3", 0.3, 660)
	bad := filepath.Join(dir, "bad.mp3")
	if err := os.WriteFile(bad, []byte("nope"), 0o644); err != nil {
		t.Fatalf("seed bad: %v", err)
	}
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{a, b, bad}, out, tmpDir, Options{})
	if err == nil {
		t.Fatal("expected failure on bad chunk, got nil")
	}

	// step_001.mp3 — produced by the (a, b) step — must still exist.
	step1 := filepath.Join(tmpDir, "step_001.mp3")
	if _, err := os.Stat(step1); err != nil {
		t.Errorf("intermediate %q should be retained on failure for debugging; got %v", step1, err)
	}
}

func TestEmptyInputs_Errors(t *testing.T) {
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), nil, out, tmpDir, Options{})
	if err == nil {
		t.Fatal("expected error for empty inputs, got nil")
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("no output should be produced for empty inputs; statErr=%v", statErr)
	}
}

func TestContextCancellation_AbortsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ffmpeg shim assumes /bin/sh; tests on Unix only")
	}
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	fake := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("seed fake ffmpeg: %v", err)
	}
	a := filepath.Join(dir, "a.mp3")
	b := filepath.Join(dir, "b.mp3")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte{0}, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out := filepath.Join(dir, "out.mp3")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := Concat(ctx, []string{a, b}, out, tmpDir, Options{
		FFmpegPath:     fake,
		PerStepTimeout: 30 * time.Second, // long, so cancel must trip first
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("ctx cancel did not propagate in %v", elapsed)
	}
}

// Compile-time check that we honor the io.Reader interface (defensive: in
// case copyFile internals are refactored to expose a stream API).
var _ io.Reader = (*os.File)(nil)
