package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// captureLogs invokes newLogger with the given flag shape, runs fn against
// the resulting logger, and returns whatever the handler wrote.
func captureLogs(t *testing.T, f rootFlags, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	logger := newLogger(&buf, &f)
	fn(logger)
	return buf.String()
}

func TestNewLogger_DefaultLevelIsInfo(t *testing.T) {
	out := captureLogs(t, rootFlags{}, func(l *slog.Logger) {
		l.Debug("debug-line")
		l.Info("info-line")
	})
	if strings.Contains(out, "debug-line") {
		t.Errorf("DEBUG should be suppressed at INFO; got %q", out)
	}
	if !strings.Contains(out, "info-line") {
		t.Errorf("INFO should be captured at INFO; got %q", out)
	}
}

func TestNewLogger_VerboseEnablesDebug(t *testing.T) {
	out := captureLogs(t, rootFlags{verbose: true}, func(l *slog.Logger) {
		l.Debug("debug-line")
	})
	if !strings.Contains(out, "debug-line") {
		t.Errorf("DEBUG should be captured under --verbose; got %q", out)
	}
}

func TestNewLogger_QuietSuppressesInfo(t *testing.T) {
	out := captureLogs(t, rootFlags{quiet: true}, func(l *slog.Logger) {
		l.Info("info-line")
		l.Warn("warn-line")
	})
	if strings.Contains(out, "info-line") {
		t.Errorf("INFO should be suppressed under --quiet; got %q", out)
	}
	if !strings.Contains(out, "warn-line") {
		t.Errorf("WARN should be captured under --quiet; got %q", out)
	}
}

func TestNewLogger_JsonHandlerEmitsJsonLines(t *testing.T) {
	out := captureLogs(t, rootFlags{json: true}, func(l *slog.Logger) {
		l.Info("hello", "key", "value")
	})
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("--json line is not valid JSON: %q (%v)", line, err)
		}
	}
}

func TestNewLogger_JsonPlusVerbose(t *testing.T) {
	out := captureLogs(t, rootFlags{json: true, verbose: true}, func(l *slog.Logger) {
		l.Debug("debug-line", "k", "v")
	})
	if !strings.Contains(out, `"msg":"debug-line"`) {
		t.Errorf("--json --verbose should emit DEBUG records as JSON; got %q", out)
	}
	if !strings.Contains(out, `"level":"DEBUG"`) {
		t.Errorf("--json --verbose should mark records DEBUG; got %q", out)
	}
}

func TestRoot_VerboseQuietRejected(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(io.Discard)
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs([]string{"--verbose", "--quiet", "doctor"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error from --verbose --quiet, got nil")
	}
	msg := err.Error() + stderr.String()
	if !strings.Contains(msg, "verbose") || !strings.Contains(msg, "quiet") {
		t.Errorf("error should mention both flags; got %q", msg)
	}
}

func TestRoot_StdoutUnaffectedByLogLevel(t *testing.T) {
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--verbose", "--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version should succeed: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != VersionLine() {
		t.Errorf("stdout should be the formatted VersionLine (got %q)", got)
	}
}

func TestRedactURL_StripsAccessToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   "https://wiki-audio.example.workers.dev/pg/x.mp3?t=abc123",
			want: "https://wiki-audio.example.workers.dev/pg/x.mp3?t=%3Credacted%3E",
		},
		{
			in:   "https://wiki-audio.example.workers.dev/pg/x.mp3",
			want: "https://wiki-audio.example.workers.dev/pg/x.mp3",
		},
		{
			in:   "https://example.com/?token=secret&debug=1",
			want: "https://example.com/?debug=1&token=%3Credacted%3E",
		},
		{
			in:   "https://example.com/?other=keep",
			want: "https://example.com/?other=keep",
		},
	}
	for _, c := range cases {
		got := RedactURL(c.in)
		if got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "abc123") || strings.Contains(got, "secret") {
			t.Errorf("RedactURL(%q) leaked secret: %q", c.in, got)
		}
	}
}

func TestRedactURL_NoSecretInRedactedOutput(t *testing.T) {
	const token = "supersecret43charsbase64urlnoPaddingxxxxxxx"
	in := "https://wiki-audio.example.workers.dev/pg/x.mp3?t=" + token
	out := RedactURL(in)
	if strings.Contains(out, token) {
		t.Errorf("redacted output still contains token: %q", out)
	}
	if !strings.Contains(out, "redacted") {
		t.Errorf("redacted output should contain 'redacted' marker: %q", out)
	}
}
