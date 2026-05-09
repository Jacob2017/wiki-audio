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

// TestTwoChunks_DurationIsExactSum — concat demuxer with -c copy
// preserves frame count exactly, so output duration equals the sum of
// inputs. (No acrossfade subtraction; that filter was retired in wa-fse
// because it required decode + re-encode at every join.)
func TestTwoChunks_DurationIsExactSum(t *testing.T) {
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
	want := probeDuration(t, a) + probeDuration(t, b)
	// libmp3lame appends a small LAME info tag at the start; ffprobe's
	// duration field is therefore precise to ~1 frame (~26 ms at
	// 44.1 kHz). 0.10s tolerance covers that without masking real
	// drift.
	if !approxEqual(got, want, 0.10) {
		t.Errorf("two-chunk duration: got %.3fs, want ≈ %.3fs (sum-of-inputs ±0.10)", got, want)
	}
}

// TestThreeChunks_DurationIsExactSum — same property at N=3 to catch
// any regression that would special-case the two-input branch.
func TestThreeChunks_DurationIsExactSum(t *testing.T) {
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
	want := probeDuration(t, a) + probeDuration(t, b) + probeDuration(t, c)
	if !approxEqual(got, want, 0.15) {
		t.Errorf("three-chunk duration: got %.3fs, want ≈ %.3fs (sum-of-inputs ±0.15)", got, want)
	}
}

// TestTenChunks_RunsToCompletionAndCleansList — N>2 on the demuxer path,
// asserting the success contract: output produced; list file removed.
func TestTenChunks_RunsToCompletionAndCleansList(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	const N = 10
	inputs := make([]string, 0, N)
	for i := 0; i < N; i++ {
		inputs = append(inputs, makeSineMP3(t, dir, "c"+strconv.Itoa(i)+".mp3", 0.5, 440+10*i))
	}
	out := filepath.Join(dir, "out.mp3")

	if err := Concat(context.Background(), inputs, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not produced: %v", err)
	}
	listPath := filepath.Join(tmpDir, "concat-list.txt")
	if _, err := os.Stat(listPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("concat-list.txt should be removed on success; statErr=%v", err)
	}
}

func TestFFmpegMissing_ReturnsClearError(t *testing.T) {
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
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

// TestFFmpegNonzeroExit_WrapsStderrTail — when ffmpeg exits non-zero,
// the wrapping error must reference ffmpeg AND surface the stderr
// tail. We use a fake-ffmpeg shim that prints to stderr then exits 1
// because (a) ffmpeg's actual concat-demuxer exit code is finicky
// across versions on demux errors, and (b) the wrapping logic is
// what we care about — the failure-mode coverage of the underlying
// ffmpeg behavior is the wrapping's contract, not ffmpeg's.
func TestFFmpegNonzeroExit_WrapsStderrTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ffmpeg shim assumes /bin/sh; tests on Unix only")
	}
	requireFFmpegStack(t) // for makeSineMP3 fixture generation
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	fake := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'fake ffmpeg failure on stderr' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("seed fake: %v", err)
	}

	a := makeSineMP3(t, dir, "a.mp3", 0.3, 440)
	b := makeSineMP3(t, dir, "b.mp3", 0.3, 660)
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{
		FFmpegPath: fake,
	})
	if err == nil {
		t.Fatal("expected error from fake ffmpeg exit 1, got nil")
	}
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("error should reference ffmpeg; got: %v", err)
	}
	if !strings.Contains(err.Error(), "stderr tail") {
		t.Errorf("error should expose stderr tail; got: %v", err)
	}
	if !strings.Contains(err.Error(), "fake ffmpeg failure") {
		t.Errorf("stderr tail should include fake ffmpeg's stderr; got: %v", err)
	}
}

// TestMissingInput_FailsFastBeforeFFmpeg — pre-flight stat catches
// missing inputs and returns a specific, actionable error WITHOUT
// invoking ffmpeg. ffmpeg's concat demuxer otherwise silently produces
// a partial output (logs "Impossible to open" but exits 0 on this
// build); the pre-flight defense-in-depth stops that footgun.
func TestMissingInput_FailsFastBeforeFFmpeg(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	good := makeSineMP3(t, dir, "good.mp3", 0.3, 440)
	missing := filepath.Join(dir, "missing.mp3") // intentionally NOT created
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{good, missing}, out, tmpDir, Options{})
	if err == nil {
		t.Fatal("expected error for missing input, got nil")
	}
	if !strings.Contains(err.Error(), "input 1 not readable") {
		t.Errorf("error should specify which index/path is missing; got: %v", err)
	}
	if !strings.Contains(err.Error(), "missing.mp3") {
		t.Errorf("error should name the missing path; got: %v", err)
	}
	// No partial output on disk.
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("no partial output should be produced; statErr=%v", statErr)
	}
}

func TestTimeoutKillsSubprocess(t *testing.T) {
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

	start := time.Now()
	err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{
		FFmpegPath: fake,
		Timeout:    200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Errorf("error should mention timeout; got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Concat returned after %v despite 200ms timeout — process not killed?", elapsed)
	}
}

// TestConcatListRetainedOnFailure — the list file is the only on-disk
// artifact ffmpeg consumed; on a non-zero ffmpeg exit it should remain
// so the operator can `cat concat-list.txt` to see the exact set of
// inputs that triggered the failure. Uses the same fake-ffmpeg shim
// as TestFFmpegNonzeroExit_WrapsStderrTail because real ffmpeg exit
// codes on the demux-error path are finicky; the contract under test
// is "list retained when the ffmpeg subprocess returns non-zero".
func TestConcatListRetainedOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ffmpeg shim assumes /bin/sh; tests on Unix only")
	}
	requireFFmpegStack(t) // for makeSineMP3 fixture
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")

	fake := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'fake ffmpeg failure on stderr' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("seed fake: %v", err)
	}

	a := makeSineMP3(t, dir, "a.mp3", 0.3, 440)
	b := makeSineMP3(t, dir, "b.mp3", 0.3, 660)
	out := filepath.Join(dir, "out.mp3")

	err := Concat(context.Background(), []string{a, b}, out, tmpDir, Options{
		FFmpegPath: fake,
	})
	if err == nil {
		t.Fatal("expected failure from fake ffmpeg exit 1, got nil")
	}

	listPath := filepath.Join(tmpDir, "concat-list.txt")
	if _, err := os.Stat(listPath); err != nil {
		t.Errorf("concat-list.txt should be retained on failure for debugging; got %v", err)
	}
	body, err := os.ReadFile(listPath)
	if err == nil {
		text := string(body)
		if !strings.Contains(text, "file '") {
			t.Errorf("list format unexpected: %q", text)
		}
		if !strings.Contains(text, filepath.Base(a)) || !strings.Contains(text, filepath.Base(b)) {
			t.Errorf("list missing one of the input file names: %q", text)
		}
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
		FFmpegPath: fake,
		Timeout:    30 * time.Second, // long, so cancel must trip first
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
