package concat

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestDefaultTimeout_HasReasonableValue pins the constant within a
// [1m, 30m] band so a future tightening PR has to surface the
// wa-4cw.8 / wa-50g / wa-fse conversation. The 1m floor reflects the
// expected wall time for a stream-copy concat of even a 100-chunk
// essay (single-digit seconds in practice) plus generous host-variance
// headroom. The 30m ceiling protects against a future "give it more
// headroom" change masking a genuine ffmpeg hang — ffmpeg should never
// take 30+ minutes to stream-copy any plausible essay. If a real
// workload needs more, the demuxer approach is wrong (probably the
// list file is malformed; that is a different bug).
//
// This test does not exercise the timeout path — that's covered by
// TestTimeoutKillsSubprocess. Its job is to lock in the value so the
// rationale stays attached to the code.
func TestDefaultTimeout_HasReasonableValue(t *testing.T) {
	const floor = 1 * time.Minute
	const ceiling = 30 * time.Minute

	if DefaultTimeout < floor {
		t.Fatalf("DefaultTimeout=%v is below the %v floor. Stream-copy concat for "+
			"any plausible essay finishes in seconds; tighter-than-1m timeouts make "+
			"the test suite flaky on slow CI hosts without protecting against any "+
			"real failure mode.", DefaultTimeout, floor)
	}
	if DefaultTimeout > ceiling {
		t.Errorf("DefaultTimeout=%v exceeds the %v ceiling. ffmpeg should never need "+
			"30+ minutes for a stream-copy demuxer call; a timeout this generous "+
			"hides genuine hangs as 'just slow'.", DefaultTimeout, ceiling)
	}
}

// TestEighteenChunks_RunsToCompletion pins the loop count where
// production broke (wa-4cw.8 spike, 18-chunk "How to Do Great Work").
// With wa-fse's stream-copy demuxer this is now a single ffmpeg call
// regardless of N, so the test is fast (~1s wall) and exercises the
// list-file generation + the demuxer's frame-count handling at a
// realistic chunk count.
//
// Fixtures are 0.5s each — long enough that libmp3lame produces
// well-formed MP3 frames at every position (at 0.2s the late chunks
// occasionally write headers ffmpeg's own demuxer rejects), short
// enough that the run finishes in seconds.
func TestEighteenChunks_RunsToCompletion(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	const N = 18
	inputs := make([]string, 0, N)
	// Vary frequency per chunk so a hypothetical silent-input regression
	// (a chunk being elided from the list file) manifests as a missing
	// frequency band in the output rather than the same tone repeated.
	for i := 0; i < N; i++ {
		freq := 220 + 30*i // ~220-730 Hz, comfortably inside MP3 range
		inputs = append(inputs, makeSineMP3(t, dir, "c"+strconv.Itoa(i)+".mp3", 0.5, freq))
	}

	out := filepath.Join(dir, "out.mp3")
	if err := Concat(context.Background(), inputs, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat over %d chunks: %v", N, err)
	}

	// Stream-copy preserves frames exactly: output duration ≈ sum of
	// input durations. Allow ~1 frame of slop per join (LAME info-tag
	// drop is the most common reason the demuxer can lose a single
	// frame at a join boundary). At 44.1 kHz / 1152-sample frames,
	// 17 joins × 26 ms ≈ 0.45s; allow 1.0s tolerance for libmp3lame
	// startup-tag variance on top of that.
	got := probeDuration(t, out)
	var want float64
	for _, p := range inputs {
		want += probeDuration(t, p)
	}
	if !approxEqual(got, want, 1.0) {
		t.Errorf("18-chunk duration: got %.3fs, want ≈ %.3fs (sum-of-inputs ±1.0)", got, want)
	}

	// On success, the concat-list.txt is removed; no step_NNN.mp3
	// files exist (those were a pairwise-reduce artifact, retired in
	// wa-fse).
	listPath := filepath.Join(tmpDir, "concat-list.txt")
	if _, err := os.Stat(listPath); err == nil {
		t.Errorf("concat-list.txt should be removed on success; still present at %s", listPath)
	}
}
