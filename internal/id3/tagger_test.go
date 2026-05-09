package id3

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
)

// makeStubMP3 writes a tiny "audio" file under a t.TempDir. The
// bytes are NOT a valid MPEG frame stream — bogem/id3v2 doesn't
// validate the audio portion, it only manipulates the ID3v2 header
// region. Tests assert tag round-trip and audio preservation, both
// of which work without a real MPEG codec.
func makeStubMP3(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stub.mp3")
	audio := bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 256) // 1 KiB
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readBackTag(t *testing.T, path string) *id3v2.Tag {
	t.Helper()
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("read-back open: %v", err)
	}
	return tag
}

// readAudioPortion returns the bytes of the file with the leading
// ID3v2 tag stripped, so tests can assert audio content survived
// across tag operations. Layout: bytes 0..2 = "ID3" magic if a v2
// tag exists; bytes 6..9 = synchsafe size of the tag; audio starts
// at offset 10 + tagSize. If no ID3v2 header exists, the whole file
// is audio.
func readAudioPortion(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 10 || string(data[:3]) != "ID3" {
		return data
	}
	// Synchsafe int: 4 bytes, 7 bits each, MSB always 0.
	size := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
	off := 10 + size
	if off > len(data) {
		t.Fatalf("computed audio offset %d exceeds file length %d", off, len(data))
	}
	return data[off:]
}

// --- per-frame round-trip ---

func TestTagWritesAllFrames(t *testing.T) {
	path := makeStubMP3(t)

	meta := TagMeta{
		Title:  "How to Do Great Work",
		Artist: "Paul Graham",
		Album:  "Paul Graham Essays",
		Year:   "2023",
		Genre:  "Audiobook",
	}
	if err := Tag(context.Background(), path, meta); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	tag := readBackTag(t, path)
	defer tag.Close()

	cases := []struct {
		name   string
		got    string
		want   string
	}{
		{"TIT2 (title)", tag.Title(), meta.Title},
		{"TPE1 (artist)", tag.Artist(), meta.Artist},
		{"TALB (album)", tag.Album(), meta.Album},
		{"TYER (year)", tag.Year(), meta.Year},
		{"TCON (genre)", tag.Genre(), meta.Genre},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// ID3 v2.3 is pinned by §8.6. Verify the saved file declares
// version 3 in the header byte (offset 3 = major version).
func TestTagWritesVersion2_3(t *testing.T) {
	path := makeStubMP3(t)
	if err := Tag(context.Background(), path, TagMeta{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:3]) != "ID3" {
		t.Fatalf("expected ID3 magic at offset 0; got %q", data[:3])
	}
	if data[3] != 3 {
		t.Errorf("ID3 major version byte = %d; want 3 (v2.3 pinned per §8.6)", data[3])
	}
}

// --- empty-field policy ---

// Empty-string fields must NOT produce empty frames. The bead asks
// us to "pick + document" — the choice is to skip empties so apps
// fall back to filename / "Unknown" rather than rendering a blank
// metadata cell.
func TestTagSkipsEmptyFields(t *testing.T) {
	path := makeStubMP3(t)

	if err := Tag(context.Background(), path, TagMeta{
		Title: "Only Title",
		// Artist, Album, Year, Genre intentionally empty
	}); err != nil {
		t.Fatal(err)
	}

	tag := readBackTag(t, path)
	defer tag.Close()

	if got := tag.Title(); got != "Only Title" {
		t.Errorf("Title: got %q, want %q", got, "Only Title")
	}
	for _, c := range []struct{ name, got string }{
		{"Artist", tag.Artist()},
		{"Album", tag.Album()},
		{"Year", tag.Year()},
		{"Genre", tag.Genre()},
	} {
		if c.got != "" {
			t.Errorf("%s: expected empty frame absent; got %q", c.name, c.got)
		}
	}
}

// --- idempotency ---

// Re-tagging with the same meta must produce a byte-identical file.
// bogem/id3v2 opens with Parse:true, so SetTitle etc. overwrite the
// existing frame slot rather than appending. A duplicate-frame bug
// would shift every byte of audio and explode the file across reruns.
func TestTagIdempotentByteIdentical(t *testing.T) {
	path := makeStubMP3(t)
	meta := TagMeta{
		Title: "Same", Artist: "Same", Album: "Same",
		Year: "2023", Genre: "Audiobook",
	}

	if err := Tag(context.Background(), path, meta); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := Tag(context.Background(), path, meta); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("re-tagging produced a different file (sizes: %d vs %d) — frames are appending, not replacing",
			len(first), len(second))
	}
}

// Re-tagging with NEW values must replace, not append. Read-back
// returns only the new values and the file size doesn't grow without
// bound across reruns.
func TestTagReplacesExistingFrames(t *testing.T) {
	path := makeStubMP3(t)

	if err := Tag(context.Background(), path, TagMeta{
		Title: "First Title", Artist: "First Artist",
	}); err != nil {
		t.Fatal(err)
	}
	sizeAfterFirst := fileSize(t, path)

	if err := Tag(context.Background(), path, TagMeta{
		Title: "Second Title", Artist: "Second Artist",
	}); err != nil {
		t.Fatal(err)
	}
	sizeAfterSecond := fileSize(t, path)

	tag := readBackTag(t, path)
	defer tag.Close()

	if got := tag.Title(); got != "Second Title" {
		t.Errorf("Title: got %q after re-tag, want 'Second Title'", got)
	}
	if got := tag.Artist(); got != "Second Artist" {
		t.Errorf("Artist: got %q after re-tag, want 'Second Artist'", got)
	}

	// Sizes are roughly equal; appended frames would balloon by ~50
	// bytes per re-tag.
	delta := sizeAfterSecond - sizeAfterFirst
	if delta < -16 || delta > 16 {
		t.Errorf("file size grew unexpectedly across re-tag: %d → %d (delta %d) — frames may be appending",
			sizeAfterFirst, sizeAfterSecond, delta)
	}
}

// --- audio preservation ---

// Tagging must not corrupt the audio bytes. The 1 KB stub payload
// after the ID3 header should match the original byte-for-byte.
func TestTagPreservesAudioBytes(t *testing.T) {
	path := makeStubMP3(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := Tag(context.Background(), path, TagMeta{
		Title: "Test", Artist: "Test", Album: "Test",
		Year: "2026", Genre: "Audiobook",
	}); err != nil {
		t.Fatal(err)
	}

	audio := readAudioPortion(t, path)
	if !bytes.Equal(audio, original) {
		t.Errorf("audio bytes changed after tagging: original %d bytes, post-tag audio %d bytes",
			len(original), len(audio))
	}
}

// --- UTF-8 ---

// PG essay titles routinely contain em-dashes and smart quotes.
// Verify a UTF-8 round-trip survives bogem's encoding.
func TestTagUTF8RoundTrip(t *testing.T) {
	path := makeStubMP3(t)
	const title = "How to Do Great Work — A Guide"

	if err := Tag(context.Background(), path, TagMeta{Title: title}); err != nil {
		t.Fatal(err)
	}
	tag := readBackTag(t, path)
	defer tag.Close()

	if got := tag.Title(); got != title {
		t.Errorf("UTF-8 round-trip failed: got %q, want %q", got, title)
	}
}

// --- size sanity ---

func TestTagSizeIncreaseBounded(t *testing.T) {
	path := makeStubMP3(t)
	before := fileSize(t, path)

	if err := Tag(context.Background(), path, TagMeta{
		Title:  "How to Do Great Work",
		Artist: "Paul Graham",
		Album:  "Paul Graham Essays",
		Year:   "2023",
		Genre:  "Audiobook",
	}); err != nil {
		t.Fatal(err)
	}
	after := fileSize(t, path)

	delta := after - before
	if delta <= 0 {
		t.Errorf("file did not grow after tagging; got delta %d", delta)
	}
	if delta > 4096 {
		t.Errorf("tag overhead %d bytes exceeds 4 KiB sanity bound", delta)
	}
}

// --- ctx cancellation ---

func TestTagContextCanceledReturnsImmediately(t *testing.T) {
	path := makeStubMP3(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	err := Tag(ctx, path, TagMeta{Title: "x"})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if err != context.Canceled {
		t.Errorf("got %v; want context.Canceled", err)
	}
}

// --- helpers ---

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
