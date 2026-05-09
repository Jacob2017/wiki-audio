package model

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

// Smoke tests for wa-kyn.1 — the data-struct bead. The full hash
// edge-case matrix (rune-vs-byte CharCount, body-under-200, etc.)
// lives in wa-kyn.9's extractor finalize tests; here we verify only
// what wa-kyn.1 itself owns: the structs marshal sensibly, the hash
// is hex of the right length and reacts to each input, and the
// schema-version constant has a defined home.

var hexHash64 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func TestBodyHashShape(t *testing.T) {
	got := BodyHash("hello", "voice", "model", "v1")
	if !hexHash64.MatchString(got) {
		t.Fatalf("BodyHash returned %q; want lowercase hex of length 64", got)
	}
}

func TestBodyHashStable(t *testing.T) {
	a := BodyHash("body", "v", "m", "p")
	b := BodyHash("body", "v", "m", "p")
	if a != b {
		t.Fatalf("hash not deterministic: %s vs %s", a, b)
	}
}

func TestBodyHashSensitiveToEachInput(t *testing.T) {
	base := BodyHash("body", "v", "m", "p")
	cases := []struct {
		name                string
		body, vid, mid, pol string
	}{
		{"body", "BODY", "v", "m", "p"},
		{"voice", "body", "V", "m", "p"},
		{"model", "body", "v", "M", "p"},
		{"policy", "body", "v", "m", "P"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BodyHash(c.body, c.vid, c.mid, c.pol)
			if got == base {
				t.Fatalf("changing %s did not change the hash", c.name)
			}
		})
	}
}

// Concatenation order is load-bearing for cache invalidation and is
// pinned by the bead description. Pin it with a known-input vector
// so a refactor that swaps the order of h.Write() calls fails loudly.
func TestBodyHashOrderIsBodyVoiceModelPolicy(t *testing.T) {
	// "ab" + "cd" + "ef" + "gh" → hash of "abcdefgh"
	got := BodyHash("ab", "cd", "ef", "gh")
	want := BodyHash("abcdefgh", "", "", "")
	if got != want {
		t.Fatalf("input concatenation order changed: got %s want %s", got, want)
	}
}

func TestManifestRoundTripsJSON(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC)
	original := Manifest{
		Version: ManifestSchemaVersion,
		Entries: map[string]ManifestEntry{
			"how-to-do-great-work": {
				Slug:            "how-to-do-great-work",
				Title:           "How to Do Great Work",
				BodyHash:        "0123456789abcdef",
				VoiceID:         "G17SuINrv2H9FC6nvetn",
				ModelID:         DefaultModelID,
				CharCount:       52481,
				ChunkCount:      14,
				DurationSeconds: 4924.5,
				FileSizeBytes:   65437184,
				R2Key:           "pg/how-to-do-great-work.mp3",
				R2ETag:          "\"abc123\"",
				GeneratedAt:     time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC),
				PublishedAt:     &pub,
			},
		},
		LastBuildAt: &pub,
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != original.Version {
		t.Errorf("Version: got %d want %d", got.Version, original.Version)
	}
	entry, ok := got.Entries["how-to-do-great-work"]
	if !ok {
		t.Fatalf("entry missing after round-trip")
	}
	if entry.Title != "How to Do Great Work" {
		t.Errorf("Title: got %q", entry.Title)
	}
	if entry.PublishedAt == nil || !entry.PublishedAt.Equal(pub) {
		t.Errorf("PublishedAt round-trip failed: got %v", entry.PublishedAt)
	}
}

// PublishedAt nil must NOT serialize as `"published_at": null` — that
// would clutter every freshly-built (unpublished) entry. The
// `omitempty` tag handles that, but a struct refactor could drop it.
func TestManifestEntryPublishedAtOmitsWhenNil(t *testing.T) {
	entry := ManifestEntry{Slug: "x"}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"published_at"`; containsField(raw, want) {
		t.Errorf("expected %s to be omitted; got %s", want, raw)
	}
}

func containsField(b []byte, key string) bool {
	for i := 0; i+len(key) <= len(b); i++ {
		if string(b[i:i+len(key)]) == key {
			return true
		}
	}
	return false
}

func TestManifestSchemaVersionPinned(t *testing.T) {
	if ManifestSchemaVersion != 1 {
		t.Fatalf("ManifestSchemaVersion = %d; bumping is a deliberate breaking change — update §6 and add a migration", ManifestSchemaVersion)
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	cases := []struct {
		name    string
		got, want any
	}{
		{"DefaultAuthor", DefaultAuthor, "Paul Graham"},
		{"DefaultModelID", DefaultModelID, "eleven_flash_v2_5"},
		{"DefaultChunkMaxChars", DefaultChunkMaxChars, 4000},
		{"DefaultRequestTimeoutS", DefaultRequestTimeoutS, 60.0},
		{"DefaultRetryAttempts", DefaultRetryAttempts, 3},
		{"DefaultRetryBackoffBase", DefaultRetryBackoffBase, 2.0},
		{"DefaultOutputFormat", DefaultOutputFormat, "mp3_44100_192"},
		{"DefaultFeedPath", DefaultFeedPath, "pg.xml"},
		{"DefaultLanguage", DefaultLanguage, "en-us"},
		{"MinBodyChars", MinBodyChars, 200},
		{"FootnotePolicyVersion", FootnotePolicyVersion, "v1"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}
}
