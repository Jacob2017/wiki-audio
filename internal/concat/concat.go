package concat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/atomic"
)

// Defaults align with PLAN §5.4.
const (
	// DefaultCrossfadeSeconds is the acrossfade duration between adjacent
	// chunks. 50ms is the §5.4 recommendation: long enough to mask the
	// chunk boundary, short enough not to be heard as a fade.
	DefaultCrossfadeSeconds = 0.05

	// DefaultPerStepTimeout caps ONE ffmpeg pairwise invocation. Total
	// runtime is bounded by ~PerStepTimeout × (N-1). 30s/step is generous
	// — even a slow Pi-class host concatenates two ~3MB MP3s well inside
	// 30s; if any step actually takes longer, ffmpeg is hung.
	DefaultPerStepTimeout = 30 * time.Second
)

// Options tweaks Concat behavior. Zero-valued fields are replaced with the
// Default* constants above.
type Options struct {
	// CrossfadeSeconds is the `d=` value passed to ffmpeg's `acrossfade`.
	CrossfadeSeconds float64

	// PerStepTimeout caps each pairwise ffmpeg invocation. The wall clock
	// total is roughly PerStepTimeout × (len(inputs)-1). The caller's
	// Context deadline (if any) further bounds runtime.
	PerStepTimeout time.Duration

	// FFmpegPath overrides the binary; default "ffmpeg" (resolved via PATH).
	FFmpegPath string

	// Logger receives one INFO record per pairwise step plus an error-level
	// record if a step fails. nil → slog.Default().
	Logger *slog.Logger
}

func (o Options) applyDefaults() Options {
	if o.CrossfadeSeconds <= 0 {
		o.CrossfadeSeconds = DefaultCrossfadeSeconds
	}
	if o.PerStepTimeout <= 0 {
		o.PerStepTimeout = DefaultPerStepTimeout
	}
	if o.FFmpegPath == "" {
		o.FFmpegPath = "ffmpeg"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Concat joins inputs into outPath using a pairwise acrossfade chain.
//
// tmpDir hosts per-step intermediates (`step_001.mp3`, `step_002.mp3`, …);
// it is created if missing, cleaned on success, and intentionally retained
// on failure so the user can inspect the partial chain.
//
// Behavior matrix:
//
//   - len(inputs) == 0 → returns an error; nothing on disk changes.
//   - len(inputs) == 1 → bytewise copy from inputs[0] to outPath; no
//     ffmpeg invocation. (A one-chunk essay shouldn't pay the encode cost.)
//   - len(inputs) >= 2 → pairwise reduce: out_i = ffmpeg(out_{i-1}, in_i).
//
// The provided ctx is honored across all steps; cancelling it stops the
// next ffmpeg call.
func Concat(ctx context.Context, inputs []string, outPath, tmpDir string, opts Options) error {
	if len(inputs) == 0 {
		return errors.New("concat: no inputs")
	}
	opts = opts.applyDefaults()

	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("concat: mkdir tmp %q: %w", tmpDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("concat: mkdir out parent: %w", err)
	}

	if len(inputs) == 1 {
		opts.Logger.Info("concat single chunk, copying", "in", inputs[0], "out", outPath)
		return copyFile(inputs[0], outPath)
	}

	cur := inputs[0]
	for i := 1; i < len(inputs); i++ {
		next := inputs[i]
		stepOut := filepath.Join(tmpDir, fmt.Sprintf("step_%03d.mp3", i))
		opts.Logger.Info("concat step",
			"step", i,
			"of", len(inputs)-1,
			"left", filepath.Base(cur),
			"right", filepath.Base(next),
			"out", filepath.Base(stepOut),
		)
		if err := acrossfadeStep(ctx, cur, next, stepOut, opts); err != nil {
			opts.Logger.Error("concat step failed",
				"step", i,
				"err", err.Error(),
				"hint", "intermediate files retained in "+tmpDir,
			)
			return fmt.Errorf("concat: step %d (%s + %s): %w",
				i, filepath.Base(cur), filepath.Base(next), err)
		}
		cur = stepOut
	}

	// Move final intermediate into place. Same-filesystem rename is the
	// fast path; cross-filesystem mounts (e.g. tmpDir on tmpfs, outPath on
	// ext4) need a copy + remove fallback.
	if err := os.Rename(cur, outPath); err != nil {
		if err := copyFile(cur, outPath); err != nil {
			return fmt.Errorf("concat: place output: %w", err)
		}
		_ = os.Remove(cur)
	}

	cleanIntermediates(tmpDir)
	return nil
}

// acrossfadeStep runs one pairwise ffmpeg call, fading `left` into `right`
// over CrossfadeSeconds and writing to `out`.
func acrossfadeStep(ctx context.Context, left, right, out string, opts Options) error {
	args := []string{
		"-y", // overwrite without prompting
		"-i", left,
		"-i", right,
		"-filter_complex", fmt.Sprintf("[0:a][1:a]acrossfade=d=%.3f", opts.CrossfadeSeconds),
		out,
	}
	return runFFmpeg(ctx, opts, args)
}

// runFFmpeg invokes ffmpeg with a per-step timeout, captures stderr, and
// translates the common failure modes into descriptive errors.
func runFFmpeg(ctx context.Context, opts Options, args []string) error {
	// Pre-flight: resolve the binary path so an absent ffmpeg becomes a
	// distinct, actionable error BEFORE we attempt fork+exec. exec.Error
	// with ErrNotFound only fires for unqualified names that LookPath
	// fails on; an absolute path to a non-existent file fails inside
	// fork/exec with fs.ErrNotExist, which we have to detect separately.
	if _, lookErr := exec.LookPath(opts.FFmpegPath); lookErr != nil {
		return fmt.Errorf("ffmpeg not found on PATH (looked for %q): %w", opts.FFmpegPath, lookErr)
	}

	stepCtx, cancel := context.WithTimeout(ctx, opts.PerStepTimeout)
	defer cancel()

	cmd := exec.CommandContext(stepCtx, opts.FFmpegPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	// Bound how long Wait() blocks on stderr pipe drainage after the
	// process is killed. Without this, a killed child whose pipe is held
	// open by an orphaned grandchild can wedge the call indefinitely
	// (we hit this with `sh -c 'sleep 30'` style tests).
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if err == nil {
		return nil
	}

	// Timeout vs. genuine non-zero exit. We check the per-step context;
	// the parent ctx cancelling presents as a different error path that
	// errors.Is will catch.
	if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffmpeg timeout after %s (cmd: %s; stderr tail: %s): %w",
			opts.PerStepTimeout,
			cmdString(opts.FFmpegPath, args),
			stderrTail(stderr.String()),
			err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("ffmpeg cancelled (cmd: %s; stderr tail: %s): %w",
			cmdString(opts.FFmpegPath, args),
			stderrTail(stderr.String()),
			err)
	}

	return fmt.Errorf("ffmpeg failed (cmd: %s; stderr tail: %s): %w",
		cmdString(opts.FFmpegPath, args),
		stderrTail(stderr.String()),
		err)
}

// copyFile is the single-chunk fast path AND the cross-filesystem rename
// fallback in Concat. Errors are wrapped with which side failed.
//
// The destination is written via the atomic helper (wa-76r.2) so a
// crash mid-copy leaves any pre-existing dst untouched. This matters
// most for the single-chunk-essay path, where dst is the final MP3
// that publish reads from — a half-copied MP3 there would round-trip
// to R2 on the next publish.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy open src %q: %w", src, err)
	}
	defer in.Close()

	if err := atomic.WriteAtomic(dst, func(w io.Writer) error {
		_, copyErr := io.Copy(w, in)
		return copyErr
	}, 0o644); err != nil {
		return fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}
	return nil
}

// cleanIntermediates removes step_NNN.mp3 files from tmpDir on success.
// Best-effort: a remove failure here is not worth surfacing — the next
// run's ffmpeg -y overwrites whatever is left.
func cleanIntermediates(tmpDir string) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "step_") && strings.HasSuffix(e.Name(), ".mp3") {
			_ = os.Remove(filepath.Join(tmpDir, e.Name()))
		}
	}
}

// cmdString renders a shell-paste-able representation of the ffmpeg call
// for inclusion in error messages and slog records.
func cmdString(bin string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, bin)
	for _, a := range args {
		// Quote anything containing whitespace or shell metacharacters so
		// the rendered command can be copy-pasted into a terminal as-is.
		if strings.ContainsAny(a, " \t\"'`$&|;<>(){}[]*?") {
			parts = append(parts, fmt.Sprintf("%q", a))
		} else {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}

// stderrTail returns up to the last 1 KiB of stderr, prefixed with "..."
// if the original was longer. ffmpeg can dump tens of KB of progress
// noise on success and only a handful of meaningful lines at the bottom
// on failure, so the tail is what carries the signal.
func stderrTail(s string) string {
	const limit = 1024
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}
