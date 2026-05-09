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

// Defaults for the lossless concat path. See package doc.go for why
// stream-copy via ffmpeg's `concat` demuxer replaces the pairwise
// acrossfade reduce that wa-50g originally tuned.
const (
	// DefaultTimeout caps the single ffmpeg invocation. Stream-copy on
	// even a 280 MB (~100 chunk) cumulative input is I/O bound and
	// should complete in single-digit seconds; 5 minutes is generous
	// headroom for slow disks, network-mounted tmpfs, or a future
	// move to a Pi-class build host. Originally `DefaultPerStepTimeout`
	// (wa-50g) when concat ran N-1 ffmpeg processes in a pairwise
	// reduce; with wa-fse there's only ONE ffmpeg call per Concat
	// invocation, so the name lost its "per step" qualifier.
	DefaultTimeout = 5 * time.Minute
)

// Options tweaks Concat behavior. Zero-valued fields are replaced with
// the Default* constants above.
type Options struct {
	// Timeout caps the ffmpeg invocation. The caller's Context deadline
	// (if any) further bounds runtime.
	Timeout time.Duration

	// FFmpegPath overrides the binary; default "ffmpeg" (resolved via PATH).
	FFmpegPath string

	// Logger receives one INFO record at start, one at success, and an
	// error-level record on failure. nil → slog.Default().
	Logger *slog.Logger
}

func (o Options) applyDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.FFmpegPath == "" {
		o.FFmpegPath = "ffmpeg"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Concat joins inputs into outPath via ffmpeg's `concat` demuxer with
// `-c copy` — frame-accurate stream copy, NO re-encoding, ZERO
// generation loss across N chunks.
//
// tmpDir hosts the concat-list (`concat-list.txt`); created if missing,
// cleaned on success, retained on failure for debugging the exact
// argument list ffmpeg saw.
//
// Behavior matrix:
//
//   - len(inputs) == 0 → returns an error; nothing on disk changes.
//   - len(inputs) == 1 → atomic bytewise copy from inputs[0] to outPath;
//     no ffmpeg invocation. (A one-chunk essay shouldn't pay the demuxer
//     cost or risk a single-frame LAME-tag drop.)
//   - len(inputs) >= 2 → single concat-demuxer ffmpeg call.
//
// The provided ctx is honored; cancelling it kills the ffmpeg subprocess.
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

	// Pre-flight: stat every input. Catches missing / unreadable files
	// BEFORE invoking ffmpeg. Without this, ffmpeg's concat demuxer
	// silently produces a partial output (it logs "Impossible to open"
	// to stderr and "Conversion failed!" but on this ffmpeg build
	// still exits 0 — we'd leak a half-essay MP3 into the publish
	// pipeline). The pre-flight check is also faster: we surface the
	// exact missing path without paying fork+exec overhead.
	for i, p := range inputs {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("concat: input %d not readable: %w", i, err)
		}
	}

	listPath := filepath.Join(tmpDir, "concat-list.txt")
	if err := writeConcatList(listPath, inputs); err != nil {
		return fmt.Errorf("concat: write list: %w", err)
	}

	opts.Logger.Info("concat",
		"n_inputs", len(inputs),
		"first", filepath.Base(inputs[0]),
		"last", filepath.Base(inputs[len(inputs)-1]),
		"list", listPath,
		"out", outPath,
	)

	args := []string{
		"-y",
		"-xerror", // fail fast on demux errors (defense-in-depth alongside pre-flight stat)
		"-f", "concat",
		"-safe", "0", // allow absolute paths in the list file
		"-i", listPath,
		"-c", "copy", // stream copy — bit-exact frames, no re-encoding
		outPath,
	}
	if err := runFFmpeg(ctx, opts, args); err != nil {
		opts.Logger.Error("concat failed",
			"err", err.Error(),
			"hint", "concat-list.txt retained at "+listPath,
		)
		return fmt.Errorf("concat: %w", err)
	}

	// Success: clean the list file. Best-effort; a remove failure here
	// is not worth surfacing because the next run's `-y` overwrites the
	// list and the output.
	_ = os.Remove(listPath)
	return nil
}

// writeConcatList materializes the file ffmpeg's `-f concat` demuxer
// reads. Format is one `file 'PATH'` per line; single-quotes inside
// PATH are escaped per ffmpeg's documented rules:
//
//	https://ffmpeg.org/ffmpeg-formats.html#concat
//
// Paths are absolute so a future move of tmpDir relative to the cwd
// doesn't silently break the demuxer.
func writeConcatList(listPath string, inputs []string) error {
	var b strings.Builder
	for _, p := range inputs {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("abspath %q: %w", p, err)
		}
		// ffmpeg concat demuxer: inside single-quoted strings, the only
		// escape is `'\''` (close quote, escaped single, reopen quote).
		escaped := strings.ReplaceAll(abs, "'", `'\''`)
		fmt.Fprintf(&b, "file '%s'\n", escaped)
	}
	return os.WriteFile(listPath, []byte(b.String()), 0o644)
}

// runFFmpeg invokes ffmpeg with a per-call timeout, captures stderr,
// and translates the common failure modes into descriptive errors.
func runFFmpeg(ctx context.Context, opts Options, args []string) error {
	// Pre-flight: resolve the binary path so an absent ffmpeg becomes a
	// distinct, actionable error BEFORE we attempt fork+exec. exec.Error
	// with ErrNotFound only fires for unqualified names that LookPath
	// fails on; an absolute path to a non-existent file fails inside
	// fork/exec with fs.ErrNotExist, which we have to detect separately.
	if _, lookErr := exec.LookPath(opts.FFmpegPath); lookErr != nil {
		return fmt.Errorf("ffmpeg not found on PATH (looked for %q): %w", opts.FFmpegPath, lookErr)
	}

	callCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, opts.FFmpegPath, args...)
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

	// Timeout vs. cancellation vs. genuine non-zero exit. We check the
	// timeout-scoped context first; the parent ctx cancelling presents
	// as a different errors.Is path.
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffmpeg timeout after %s (cmd: %s; stderr tail: %s): %w",
			opts.Timeout,
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

// copyFile is the single-chunk fast path. The destination is written
// via the atomic helper (wa-76r.2) so a crash mid-copy leaves any
// pre-existing dst untouched. This matters most for the single-chunk-
// essay path, where dst is the final MP3 that publish reads from — a
// half-copied MP3 there would round-trip to R2 on the next publish.
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
