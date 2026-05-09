package id3

import (
	"context"
	"fmt"

	"github.com/bogem/id3v2/v2"
)

// TagMeta carries the §5.5 ID3 v2.3 frame values for one
// concatenated essay MP3. Fields map 1:1 onto frame IDs:
//
//	Title  → TIT2
//	Artist → TPE1
//	Album  → TALB
//	Year   → TYER  (ID3 v2.3 stores year as a string text frame
//	                — "2023", not a numeric type)
//	Genre  → TCON
//
// Empty fields are skipped — Tag does not write empty frames. The
// bead's "pick + document" choice on this is: writing an empty
// frame would surface in podcast apps as a blank field, which is
// worse than leaving the frame absent (apps default to a sensible
// fallback). Callers that want to clear a previously-set field
// must overwrite with a sentinel value, not pass "".
type TagMeta struct {
	Title  string
	Artist string
	Album  string
	Year   string
	Genre  string
}

// Tag writes meta as ID3 v2.3 frames onto the MP3 at mp3Path.
//
// Idempotent: re-tagging an already-tagged file replaces frame
// values rather than appending. bogem/id3v2 opens the file with
// the existing tag pre-loaded (Parse: true), so SetTitle / etc.
// overwrite in place; Save rewrites the tag header without
// duplicating frames.
//
// ID3 version is pinned to v2.3 per §8.6 ("Older media players
// choke on v2.4. v2.3 is universally supported."). bogem defaults
// to v2.4 on a fresh tag — SetVersion(3) is the load-bearing call.
//
// Text encoding is pinned to UTF-16 with BOM. ID3 v2.3 only
// supports ISO-8859-1 (encoding byte 0) and UTF-16 (byte 1); UTF-8
// was added in v2.4. PG essays routinely contain em-dashes and
// smart quotes that ISO-8859-1 cannot encode, so UTF-16 is
// mandatory at v2.3. SetDefaultEncoding overrides bogem's ISO
// default; subsequent SetTitle/etc. calls use the new default.
//
// ctx is checked once at entry. The bogem API is synchronous and
// fast (a 60 MB MP3 re-tags in single-digit ms), so ctx is not
// threaded into the file I/O. Reserved for future cancellation
// hooks if a parallel batch tagger is added.
func Tag(ctx context.Context, mp3Path string, meta TagMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tag, err := id3v2.Open(mp3Path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("id3: open %s: %w", mp3Path, err)
	}
	defer tag.Close()

	tag.SetVersion(3)
	tag.SetDefaultEncoding(id3v2.EncodingUTF16)

	if meta.Title != "" {
		tag.SetTitle(meta.Title)
	}
	if meta.Artist != "" {
		tag.SetArtist(meta.Artist)
	}
	if meta.Album != "" {
		tag.SetAlbum(meta.Album)
	}
	if meta.Year != "" {
		tag.SetYear(meta.Year)
	}
	if meta.Genre != "" {
		tag.SetGenre(meta.Genre)
	}

	if err := tag.Save(); err != nil {
		return fmt.Errorf("id3: save %s: %w", mp3Path, err)
	}
	return nil
}
