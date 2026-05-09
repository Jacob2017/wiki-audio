package concat

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestDefaultPerStepTimeout_AccommodatesObservedRealLoad pins the
// constant within a [4m, 30m] band so a future "tighten this" PR has
// to surface the wa-4cw.8 / wa-50g conversation. The 4m floor reflects
// the worst observed real load (41.2s on step 17 of an 18-chunk PG
// essay) plus ~6× safety margin. The 30m ceiling protects against a
// future "give it more headroom" change masking a real ffmpeg hang
// (the symptom the original 30s value was trying to catch).
//
// This test does not exercise the timeout path — that's covered by
// TestTimeoutKillsSubprocess. Its job is to lock in the load-bearing
// constant value so the rationale stays attached to the code.
func TestDefaultPerStepTimeout_AccommodatesObservedRealLoad(t *testing.T) {
	const floor = 4 * time.Minute
	const ceiling = 30 * time.Minute

	if DefaultPerStepTimeout < floor {
		t.Fatalf("DefaultPerStepTimeout=%v is below the %v floor. The wa-4cw.8 spike "+
			"(see wa-50g) measured 41.2s on step 17 of an 18-chunk essay; tightening "+
			"below 4m re-introduces that production failure mode.", DefaultPerStepTimeout, floor)
	}
	if DefaultPerStepTimeout > ceiling {
		t.Errorf("DefaultPerStepTimeout=%v exceeds the %v ceiling. A timeout this long "+
			"hides genuine ffmpeg hangs as 'just slow'; if the real workload needs more "+
			"than 30m per pairwise step, the per-step approach is wrong (re-encode? "+
			"single-shot concat filter?).", DefaultPerStepTimeout, ceiling)
	}
}

// TestEighteenChunks_RunsToCompletion exercises the loop count where
// production broke (wa-4cw.8 spike died at step 13 of 17 = N-1 = 17).
// Fixtures are intentionally tiny so the test runs in seconds. The
// timeout regression itself can't be reproduced at this fixture size;
// the test's job is to pin the loop semantics at N=18, exercising the
// step_NNN.mp3 naming, the rename / cross-filesystem fallback at the
// final step, and the cleanup-on-success path under a non-trivial
// chunk count.
//
// We deliberately do NOT assert output duration here. Existing
// TestTwoChunks_DurationLessByCrossfade and TestThreeChunks_PairwiseDuration
// pin the duration math at fixtures large enough to dominate MP3 frame
// noise. At 0.2s × 18 chunks the duration measurement is dominated by
// libmp3lame encoder delay + frame quantization rather than acrossfade
// arithmetic — so a duration assertion here would be flaky without
// adding signal that those existing tests don't already cover.
func TestEighteenChunks_RunsToCompletion(t *testing.T) {
	requireFFmpegStack(t)
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "tmp")
	const N = 18
	inputs := make([]string, 0, N)
	// Vary frequency per chunk so a hypothetical silent-step regression
	// (a chunk being elided by buggy concat) manifests as audible
	// boundary effects rather than the same tone repeated. Fixtures are
	// 0.5s each — long enough that libmp3lame produces well-formed MP3
	// frames at every concat step (at 0.2s the late steps occasionally
	// write headers ffmpeg's own demuxer rejects), short enough that
	// the 17-step run finishes in ~10s.
	for i := 0; i < N; i++ {
		freq := 220 + 30*i // ~220-730 Hz, comfortably inside MP3 range
		inputs = append(inputs, makeSineMP3(t, dir, "c"+strconv.Itoa(i)+".mp3", 0.5, freq))
	}

	out := filepath.Join(dir, "out.mp3")
	if err := Concat(context.Background(), inputs, out, tmpDir, Options{}); err != nil {
		t.Fatalf("Concat over %d chunks: %v", N, err)
	}

	// Output exists, non-empty, ffprobe-parseable. Same shape as
	// TestTenChunks_RunsToCompletionAndCleansTmp's success contract.
	if d := probeDuration(t, out); d <= 0 {
		t.Errorf("output duration must be positive; got %.3fs", d)
	}

	// On success, no step_NNN.mp3 intermediates remain.
	entries, err := filepath.Glob(filepath.Join(tmpDir, "step_*.mp3"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("intermediates not cleaned on success: %d remaining (%v)", len(entries), entries)
	}
}
