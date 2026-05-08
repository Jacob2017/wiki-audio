package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/cobra"
)

// errDoctorChecksFailed is returned by `wiki-audio doctor` when one
// or more checks failed. Cobra exits non-zero on any non-nil error
// from RunE, satisfying the bead's "exits non-zero" contract.
// SilenceErrors+SilenceUsage on the root command keep the
// human-readable diagnostics on stdout from being followed by extra
// noise on stderr.
var errDoctorChecksFailed = errors.New("doctor: one or more checks failed")

// doctorOpts collects values that the live checks talk to. Pulled
// out so tests can swap in httptest URLs without monkey-patching
// package globals.
type doctorOpts struct {
	configPath        string
	envPath           string
	elevenLabsBaseURL string  // override https://api.elevenlabs.io
	r2EndpointFormat  string  // override "%s.r2.cloudflarestorage.com"
	httpClient        *http.Client
}

func defaultDoctorOpts(configPath, envPath string) doctorOpts {
	return doctorOpts{
		configPath:        configPath,
		envPath:           envPath,
		elevenLabsBaseURL: "https://api.elevenlabs.io",
		r2EndpointFormat:  "%s.r2.cloudflarestorage.com",
		httpClient:        &http.Client{Timeout: 10 * time.Second},
	}
}

// checkResult is a single line in the §3 output table. OK drives the
// glyph (✓ vs ✗) and the final exit code.
type checkResult struct {
	Label  string
	OK     bool
	Status string
}

// formatCheck renders a checkResult to the §3 output shape:
//
//	"<label, left-padded to 20>✓ <status>"
//
// 20 chars accommodates the longest label ("ElevenLabs API"). The §3
// example uses a fixed left-pad rather than tabwriter dynamic widths
// to keep diff-readable test goldens stable.
func formatCheck(r checkResult) string {
	glyph := "✓"
	if !r.OK {
		glyph = "✗"
	}
	return fmt.Sprintf("%-20s%s %s", r.Label, glyph, r.Status)
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Verify config + secrets + dependency reachability",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Root().PersistentFlags().GetString("config")
			envPath, _ := cmd.Root().PersistentFlags().GetString("env")
			envLocal, _ := cmd.Root().PersistentFlags().GetBool("env-local")
			if envLocal {
				envPath = ".env"
			}
			ok := runDoctor(cmd.Context(), cmd.OutOrStdout(),
				defaultDoctorOpts(cfgPath, envPath))
			if !ok {
				return errDoctorChecksFailed
			}
			return nil
		},
	}
}

// runDoctor executes the §3 seven-check matrix. Returns true iff
// every check passed. Writes one line per check to w in the §3
// format, then a summary line.
//
// Failure aggregation: a failing check does NOT short-circuit the
// remaining ones. Operators want a full diagnostic in one shot; if
// .env permissions are wrong AND ffmpeg is missing AND R2 is
// misconfigured, they should see all three on a single run rather
// than fix-rerun-fix-rerun.
//
// Skip-not-fail semantic: when check N requires a successful prereq
// from check M (e.g. ElevenLabs needs the API key from .env), and M
// failed, N is rendered with ✗ and a "skipped: <reason>" status. The
// run continues. Counts as a failed check for the exit code.
func runDoctor(ctx context.Context, w io.Writer, opts doctorOpts) bool {
	var results []checkResult

	cfg, cfgRes := checkConfig(opts.configPath)
	results = append(results, cfgRes)

	envRes := checkEnv(opts.envPath)
	results = append(results, envRes)

	results = append(results, checkFfmpeg())

	if cfg != nil {
		results = append(results, checkSourceDir(cfg.Wiki.SourceDir))
	} else {
		results = append(results, skipped("wiki source dir", "config invalid"))
	}

	if cfg != nil && envRes.OK {
		results = append(results, checkElevenLabs(ctx, opts, cfg.TTS))
	} else {
		results = append(results, skipped("ElevenLabs API", "requires config + .env"))
	}

	if cfg != nil && envRes.OK {
		results = append(results, checkR2(ctx, opts, cfg.R2))
	} else {
		results = append(results, skipped("R2 bucket", "requires config + .env"))
	}

	if cfg != nil && envRes.OK {
		results = append(results, checkWorker(ctx, opts, cfg.Feed.BaseURL))
	} else {
		results = append(results, skipped("worker access", "requires config + .env"))
	}

	allOK := true
	for _, r := range results {
		fmt.Fprintln(w, formatCheck(r))
		if !r.OK {
			allOK = false
		}
	}
	if allOK {
		fmt.Fprintln(w, "all checks passed.")
	} else {
		fmt.Fprintln(w, "checks failed; see ✗ lines above.")
	}
	return allOK
}

func skipped(label, reason string) checkResult {
	return checkResult{Label: label, OK: false, Status: "skipped: " + reason}
}

// --- check 1: config.toml ---

func checkConfig(path string) (*model.Config, checkResult) {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, checkResult{
			Label:  "config.toml",
			OK:     false,
			Status: shorten(err.Error(), 200),
		}
	}
	return cfg, checkResult{
		Label:  "config.toml",
		OK:     true,
		Status: path,
	}
}

// --- check 2: .env ---

func checkEnv(path string) checkResult {
	if err := config.LoadEnv(path); err != nil {
		return checkResult{
			Label:  ".env",
			OK:     false,
			Status: shorten(err.Error(), 200),
		}
	}
	return checkResult{
		Label:  ".env",
		OK:     true,
		Status: fmt.Sprintf("all %d required env vars present", len(config.RequiredEnvVars)),
	}
}

// --- check 3: ffmpeg ---

func checkFfmpeg() checkResult {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return checkResult{
			Label:  "ffmpeg",
			OK:     false,
			Status: "not found in PATH (install: brew install ffmpeg / apt install ffmpeg)",
		}
	}
	out, _ := exec.Command(bin, "-version").Output()
	version := "(unknown version)"
	if firstLine := strings.SplitN(string(out), "\n", 2)[0]; firstLine != "" {
		version = firstLine
	}
	return checkResult{
		Label:  "ffmpeg",
		OK:     true,
		Status: shorten(version, 80),
	}
}

// --- check 4: wiki source dir ---

func checkSourceDir(dir string) checkResult {
	info, err := os.Stat(dir)
	if err != nil {
		return checkResult{
			Label:  "wiki source dir",
			OK:     false,
			Status: fmt.Sprintf("stat %s: %v", dir, err),
		}
	}
	if !info.IsDir() {
		return checkResult{
			Label:  "wiki source dir",
			OK:     false,
			Status: fmt.Sprintf("%s is not a directory", dir),
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return checkResult{
			Label:  "wiki source dir",
			OK:     false,
			Status: fmt.Sprintf("read %s: %v", dir, err),
		}
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			count++
		}
	}
	if count == 0 {
		return checkResult{
			Label:  "wiki source dir",
			OK:     false,
			Status: fmt.Sprintf("no .md files in %s", dir),
		}
	}
	return checkResult{
		Label:  "wiki source dir",
		OK:     true,
		Status: fmt.Sprintf("%d .md files at %s", count, dir),
	}
}

// --- check 5: ElevenLabs API ---

type elSubscription struct {
	CharacterCount int `json:"character_count"`
	CharacterLimit int `json:"character_limit"`
}

type elVoice struct {
	Name string `json:"name"`
}

func checkElevenLabs(ctx context.Context, opts doctorOpts, tts model.TTSConfig) checkResult {
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		return skipped("ElevenLabs API", "ELEVENLABS_API_KEY empty")
	}

	subURL := opts.elevenLabsBaseURL + "/v1/user/subscription"
	sub, err := elGet[elSubscription](ctx, opts.httpClient, subURL, apiKey)
	if err != nil {
		return checkResult{
			Label:  "ElevenLabs API",
			OK:     false,
			Status: fmt.Sprintf("auth: %v", err),
		}
	}
	credits := sub.CharacterLimit - sub.CharacterCount

	if tts.VoiceID == "" {
		return checkResult{
			Label: "ElevenLabs API",
			OK:    false,
			Status: fmt.Sprintf("authenticated, %s credits remaining; "+
				"[tts].voice_id is empty so voice reachability not checked",
				humanCommas(credits)),
		}
	}

	voiceURL := opts.elevenLabsBaseURL + "/v1/voices/" + url.PathEscape(tts.VoiceID)
	voice, err := elGet[elVoice](ctx, opts.httpClient, voiceURL, apiKey)
	if err != nil {
		return checkResult{
			Label: "ElevenLabs API",
			OK:    false,
			Status: fmt.Sprintf("authenticated, %s credits remaining, but voice %q unreachable: %v",
				humanCommas(credits), tts.VoiceID, err),
		}
	}
	return checkResult{
		Label: "ElevenLabs API",
		OK:    true,
		Status: fmt.Sprintf("authenticated, voice %q reachable, %s credits remaining on plan",
			voice.Name, humanCommas(credits)),
	}
}

func elGet[T any](ctx context.Context, c *http.Client, url, apiKey string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return zero, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// --- check 6: R2 bucket ---

func checkR2(ctx context.Context, opts doctorOpts, r2 model.R2Config) checkResult {
	if r2.AccountID == "" || r2.Bucket == "" {
		return checkResult{
			Label: "R2 bucket", OK: false,
			Status: "[r2].account_id and [r2].bucket required",
		}
	}
	endpoint := fmt.Sprintf(opts.r2EndpointFormat, r2.AccountID)
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			os.Getenv("R2_ACCESS_KEY_ID"),
			os.Getenv("R2_SECRET_ACCESS_KEY"),
			""),
		Secure: true,
	})
	if err != nil {
		return checkResult{Label: "R2 bucket", OK: false,
			Status: fmt.Sprintf("client init: %v", err)}
	}

	exists, err := client.BucketExists(ctx, r2.Bucket)
	if err != nil {
		return checkResult{Label: "R2 bucket", OK: false,
			Status: fmt.Sprintf("HEAD %s: %v", r2.Bucket, err)}
	}
	if !exists {
		return checkResult{Label: "R2 bucket", OK: false,
			Status: fmt.Sprintf("bucket %s does not exist (or auth lacks ListBucket)", r2.Bucket)}
	}

	count := 0
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for range client.ListObjects(listCtx, r2.Bucket, minio.ListObjectsOptions{Recursive: true}) {
		count++
		if count > 10000 {
			break
		}
	}
	return checkResult{
		Label:  "R2 bucket",
		OK:     true,
		Status: fmt.Sprintf("%s (private, %d objects)", r2.Bucket, count),
	}
}

// --- check 7: Worker access contract ---

// checkWorker fires three GETs at the configured Worker base URL:
//
//   1. bare URL (no token)               → expect 403
//   2. wrong token (?t=garbage)          → expect 403
//   3. correct token + missing key path  → expect 404
//
// If all three match expected status, the worker's constant-time
// token gate is wired correctly. Pinned by wa-3ia.5 as the live
// integration contract.
func checkWorker(ctx context.Context, opts doctorOpts, baseURL string) checkResult {
	if baseURL == "" {
		return checkResult{Label: "worker access", OK: false,
			Status: "[feed].base_url is empty"}
	}
	token := os.Getenv("WIKI_AUDIO_ACCESS_TOKEN")
	if token == "" {
		return skipped("worker access", "WIKI_AUDIO_ACCESS_TOKEN empty")
	}

	probePath := fmt.Sprintf("/wa-doctor-probe-%d", time.Now().UnixNano())
	cases := []struct {
		name   string
		url    string
		expect int
	}{
		{"bare URL", baseURL + probePath, http.StatusForbidden},
		{"wrong token", baseURL + probePath + "?t=garbage", http.StatusForbidden},
		{"right token + missing key", baseURL + probePath + "?t=" + url.QueryEscape(token), http.StatusNotFound},
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			return checkResult{Label: "worker access", OK: false,
				Status: fmt.Sprintf("%s: build req: %v", c.name, err)}
		}
		resp, err := opts.httpClient.Do(req)
		if err != nil {
			return checkResult{Label: "worker access", OK: false,
				Status: fmt.Sprintf("%s: %v", c.name, err)}
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.expect {
			return checkResult{Label: "worker access", OK: false,
				Status: fmt.Sprintf("%s: got %d, want %d", c.name, resp.StatusCode, c.expect)}
		}
	}
	return checkResult{
		Label: "worker access",
		OK:    true,
		Status: fmt.Sprintf("%s returns 403 without token, 404 with token",
			baseURL),
	}
}

// --- helpers ---

// shorten truncates s to maxLen bytes (including the ellipsis when
// added). Defends against a verbose error message blowing up the
// doctor table layout. Trims at a UTF-8 boundary by walking back if
// the cut would land mid-rune.
func shorten(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	const ell = "…" // 3 bytes in UTF-8
	if maxLen <= len(ell) {
		return ell[:maxLen]
	}
	cut := maxLen - len(ell)
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + ell
}

// humanCommas formats an int with thousand separators ("487222" →
// "487,222") matching the §3 output sample. Stdlib doesn't ship a
// commas helper for int; this is small enough to inline.
func humanCommas(n int) string {
	if n < 0 {
		return "-" + humanCommas(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	first := len(s) % 3
	if first == 0 {
		first = 3
	}
	parts := []string{s[:first]}
	for i := first; i < len(s); i += 3 {
		parts = append(parts, s[i:i+3])
	}
	return strings.Join(parts, ",")
}
