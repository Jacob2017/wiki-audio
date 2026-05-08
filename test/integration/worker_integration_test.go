//go:build integration

package integration

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/testutil"
)

const (
	envWorkerURL  = "WIKI_AUDIO_WORKER_URL"
	envToken      = "WIKI_AUDIO_ACCESS_TOKEN"
	envFixtureKey = "WIKI_AUDIO_FIXTURE_KEY"

	defaultWorkerURL  = "https://wiki-audio.jabyrne.workers.dev"
	defaultFixtureKey = "pg/how-to-do-great-work.mp3"

	// per-request budget; the Worker is on a global edge and should respond
	// well inside this even on the worst path.
	requestTimeout = 10 * time.Second
)

func TestMain(m *testing.M) {
	gates := []string{
		"build_tag=integration",
		fmt.Sprintf("env_%s=%q", testutil.IntegrationEnv, os.Getenv(testutil.IntegrationEnv)),
		fmt.Sprintf("worker_url=%s", workerBaseURL()),
	}
	slog.Info("integration suite startup", "gates", gates)
	os.Exit(m.Run())
}

func workerBaseURL() string {
	if v := os.Getenv(envWorkerURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultWorkerURL
}

// requireWorkerCreds asserts the env required to talk to the Worker is set.
func requireWorkerCreds(t *testing.T) {
	t.Helper()
	testutil.RequireIntegration(t)
	testutil.RequireCredentials(t, envToken)
}

// httpClient returns a per-test http.Client with a request timeout. We use
// a fresh client per test so a hung connection in one test cannot starve
// another.
func httpClient() *http.Client {
	return &http.Client{Timeout: requestTimeout}
}

// fetch performs a single GET and returns status, body, and elapsed time.
// The caller closes nothing — the response body is fully drained and closed
// inside.
func fetch(t *testing.T, target string) (status int, body []byte, headers http.Header, elapsed time.Duration) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	start := time.Now()
	resp, err := httpClient().Do(req)
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("GET %s: %v", redactToken(target), err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, body, resp.Header, elapsed
}

// redactToken strips a `?t=` value from a URL string for safe logging.
func redactToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Has("t") {
		q.Set("t", "<redacted>")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func urlNoToken(t *testing.T, key string) string {
	t.Helper()
	return workerBaseURL() + "/" + strings.TrimLeft(key, "/")
}

func urlWithToken(t *testing.T, key, token string) string {
	t.Helper()
	q := url.Values{}
	q.Set("t", token)
	return urlNoToken(t, key) + "?" + q.Encode()
}

func token(t *testing.T) string {
	t.Helper()
	v := os.Getenv(envToken)
	if v == "" {
		t.Fatalf("%s is empty after RequireCredentials passed — bug in helper", envToken)
	}
	return v
}

func fixtureKey() string {
	if v := os.Getenv(envFixtureKey); v != "" {
		return v
	}
	return defaultFixtureKey
}

// TestWorker_NoTokenReturns403 — bare URL must 403.
func TestWorker_NoTokenReturns403(t *testing.T) {
	requireWorkerCreds(t)
	log := slog.With("test", t.Name(), "service", "worker", "status_expected", 403)

	target := urlNoToken(t, fixtureKey())
	status, body, _, elapsed := fetch(t, target)

	log.Info("contract probe", "status_got", status, "elapsed_ms", elapsed.Milliseconds())
	if status != http.StatusForbidden {
		t.Errorf("bare URL should 403, got %d", status)
	}
	if strings.Contains(string(body), "/") || strings.Contains(string(body), "wiki-audio") {
		t.Errorf("403 body must not leak bucket info; got %q", string(body))
	}
}

// TestWorker_WrongTokenReturns403 — same status AND same body as
// TestWorker_NoTokenReturns403 (no token-format oracle).
func TestWorker_WrongTokenReturns403(t *testing.T) {
	requireWorkerCreds(t)
	log := slog.With("test", t.Name(), "service", "worker", "status_expected", 403)

	noTokenStatus, noTokenBody, _, _ := fetch(t, urlNoToken(t, fixtureKey()))
	wrongStatus, wrongBody, _, elapsed := fetch(t, urlWithToken(t, fixtureKey(), "garbage"))

	log.Info("contract probe", "status_got", wrongStatus, "elapsed_ms", elapsed.Milliseconds())
	if wrongStatus != http.StatusForbidden {
		t.Errorf("wrong token should 403, got %d", wrongStatus)
	}
	if noTokenStatus != http.StatusForbidden {
		t.Fatalf("baseline no-token request returned %d (expected 403); test cannot continue", noTokenStatus)
	}
	if string(noTokenBody) != string(wrongBody) {
		t.Errorf("no-token vs wrong-token bodies differ — token-format oracle leak.\n  no_token=%q\n  wrong=%q",
			string(noTokenBody), string(wrongBody))
	}
}

// TestWorker_RightTokenMissingKeyReturns404 — the security boundary. After
// token validation passes, missing keys leak as 404 (not 403). This is the
// signal that proves the gate is structured correctly: a 403 here would
// mean the Worker is short-circuiting before token validation.
func TestWorker_RightTokenMissingKeyReturns404(t *testing.T) {
	requireWorkerCreds(t)
	log := slog.With("test", t.Name(), "service", "worker", "status_expected", 404)

	missing := fmt.Sprintf("does-not-exist-%d.mp3", time.Now().UnixNano())
	target := urlWithToken(t, missing, token(t))
	status, _, _, elapsed := fetch(t, target)

	log.Info("contract probe", "status_got", status, "elapsed_ms", elapsed.Milliseconds())
	if status != http.StatusNotFound {
		t.Errorf("right token + missing key should 404, got %d (a 403 here means token check is short-circuiting)", status)
	}
}

// TestWorker_RightTokenExistingKeyReturns200 — happy-path object fetch.
// Skipped (with a clear reason) if the fixture key is not yet in R2; this
// is benign before Phase G uploads any audio. Override with
// WIKI_AUDIO_FIXTURE_KEY to point at an existing object.
func TestWorker_RightTokenExistingKeyReturns200(t *testing.T) {
	requireWorkerCreds(t)
	log := slog.With("test", t.Name(), "service", "worker", "status_expected", 200)

	target := urlWithToken(t, fixtureKey(), token(t))
	status, body, headers, elapsed := fetch(t, target)
	log.Info("contract probe", "status_got", status, "elapsed_ms", elapsed.Milliseconds(), "fixture_key", fixtureKey())

	if status == http.StatusNotFound {
		t.Skipf("fixture key %q not in R2 yet; set %s to an existing key once Phase G has uploaded audio", fixtureKey(), envFixtureKey)
	}
	if status != http.StatusOK {
		t.Fatalf("right token + real key should 200, got %d", status)
	}
	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "audio/") {
		t.Errorf("Content-Type should start with audio/; got %q", ct)
	}
	if cc := headers.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control must contain 'private' (defends against edge-cache leak across token values); got %q", cc)
	}
	if len(body) == 0 {
		t.Errorf("200 with empty body — Worker streaming returned nothing")
	}
}

// TestWorker_RangeRequestSupported — partial-content with a Range header.
// Same skip behavior as the 200 test when the fixture key is missing.
func TestWorker_RangeRequestSupported(t *testing.T) {
	requireWorkerCreds(t)
	log := slog.With("test", t.Name(), "service", "worker", "status_expected", 206)

	target := urlWithToken(t, fixtureKey(), token(t))

	// First, get the full size via a normal GET (skip if missing).
	status, fullBody, _, _ := fetch(t, target)
	if status == http.StatusNotFound {
		t.Skipf("fixture key %q not in R2 yet; range test needs a real object", fixtureKey())
	}
	if status != http.StatusOK {
		t.Fatalf("baseline GET returned %d; expected 200", status)
	}
	fullSize := len(fullBody)
	if fullSize < 64 {
		t.Skipf("fixture %q is %d bytes, too small for a meaningful range test", fixtureKey(), fullSize)
	}

	// Request bytes 0-31.
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build range request: %v", err)
	}
	req.Header.Set("Range", "bytes=0-31")
	start := time.Now()
	resp, err := httpClient().Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("range GET: %v", err)
	}
	defer resp.Body.Close()
	rangeBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read range body: %v", err)
	}
	log.Info("range probe", "status_got", resp.StatusCode, "elapsed_ms", elapsed.Milliseconds(), "got_bytes", len(rangeBody))

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("Range request should 206, got %d", resp.StatusCode)
	}
	if got := len(rangeBody); got != 32 {
		t.Errorf("Range bytes=0-31 should return 32 bytes, got %d", got)
	}
	if cr := resp.Header.Get("Content-Range"); cr == "" {
		t.Errorf("206 response must carry a Content-Range header")
	} else {
		expectedSuffix := "/" + strconv.Itoa(fullSize)
		if !strings.HasSuffix(cr, expectedSuffix) {
			t.Errorf("Content-Range %q should end with %q (full object size)", cr, expectedSuffix)
		}
	}
}

// TestWorker_TimingWithinOneStddev — heuristic timing-oracle check. Sends
// 100 wrong-token requests with random tokens and asserts that latency
// stddev is small relative to the mean. A timing oracle in
// timingSafeEqual would manifest as a per-byte cliff; our threshold is
// permissive (the public internet adds far more jitter than any
// per-character branch). The intent is "obvious leakage" detection,
// not cryptographic guarantees — see wa-3ia.5 spec.
func TestWorker_TimingWithinOneStddev(t *testing.T) {
	requireWorkerCreds(t)
	if testing.Short() {
		t.Skip("timing test sends 100 requests; skipped under -short")
	}
	log := slog.With("test", t.Name(), "service", "worker")

	const samples = 100
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		fakeToken := randomToken(t, 43)
		target := urlWithToken(t, fixtureKey(), fakeToken)
		status, _, _, elapsed := fetch(t, target)
		if status != http.StatusForbidden {
			t.Fatalf("wrong-token sample %d returned %d; expected 403 — gate is broken, abort timing test", i, status)
		}
		durations = append(durations, elapsed)
	}

	mean, stddev, median := stats(durations)
	ratio := float64(stddev) / float64(mean)
	log.Info("timing summary",
		"samples", samples,
		"mean_ms", mean.Milliseconds(),
		"median_ms", median.Milliseconds(),
		"stddev_ms", stddev.Milliseconds(),
		"stddev_over_mean", ratio,
	)

	// Threshold is forgiving: real jitter on the public internet is high.
	// We are looking for catastrophic divergence (e.g. 10x), not subtle
	// per-byte timing leakage.
	const ratioThreshold = 1.5
	if ratio > ratioThreshold {
		t.Errorf("timing stddev/mean = %.3f exceeds threshold %.3f — possible timing oracle or network instability; investigate", ratio, ratioThreshold)
	}

	// Sanity: median should be within an order of magnitude of mean. If
	// they diverge dramatically we have outliers driving mean up.
	if mean > 5*median || median > 5*mean {
		t.Errorf("mean=%v vs median=%v differ by >5x — heavy-tail latency, repeat in calmer conditions", mean, median)
	}
}

// randomToken produces a base64url string of n chars for use as a wrong
// token. Length matches the real token's 43-char shape so the Worker's
// length-equality short-circuit takes the same path as a real attack
// would.
func randomToken(t *testing.T, n int) string {
	t.Helper()
	bytesNeeded := (n*6 + 7) / 8 // base64 input bytes for n output chars
	buf := make([]byte, bytesNeeded)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	s := base64.RawURLEncoding.EncodeToString(buf)
	if len(s) < n {
		t.Fatalf("randomToken: encoded len %d < requested %d", len(s), n)
	}
	return s[:n]
}

// stats returns mean, population stddev, and median of a duration slice.
func stats(ds []time.Duration) (mean, stddev, median time.Duration) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	mean = sum / time.Duration(len(ds))

	var sqSum float64
	for _, d := range ds {
		diff := float64(d - mean)
		sqSum += diff * diff
	}
	stddev = time.Duration(math.Sqrt(sqSum / float64(len(ds))))

	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median = sorted[len(sorted)/2]
	return mean, stddev, median
}
