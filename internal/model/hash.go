package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// FootnotePolicyVersion is the cache-bust knob for the §5.1 step 8
// footnote-weaving algorithm. Bumping it (e.g. "v1" → "v2") changes
// every body_hash and forces every essay to regenerate on the next
// build — the right behavior whenever the algorithm itself changes
// (e.g. switching from paragraph-end to inline-aside footnotes).
const FootnotePolicyVersion = "v1"

// BodyHash computes the §2 cache-busting hash for a cleaned essay.
//
// The exact byte sequence fed to sha256 is plain UTF-8 concatenation,
// no separator, no length-prefixing:
//
//	body || voiceID || modelID || footnotePolicyVersion
//
// Bundling voiceID, modelID, and footnotePolicyVersion into the hash
// makes cache invalidation automatic: any one of them changing
// invalidates every cached MP3 without a manual --force pass. Tests
// that need to verify "different X → different hash" must call
// BodyHash directly with each variant rather than wrapping a helper
// that hides the inputs.
//
// OutputFormat (e.g. "mp3_44100_64" vs "mp3_44100_128") is
// deliberately NOT included — bitrate changes are rare and require an
// explicit --force rebuild per §2. See wa-6la finding F11.
func BodyHash(body, voiceID, modelID, footnotePolicyVersion string) string {
	h := sha256.New()
	h.Write([]byte(body))
	h.Write([]byte(voiceID))
	h.Write([]byte(modelID))
	h.Write([]byte(footnotePolicyVersion))
	return hex.EncodeToString(h.Sum(nil))
}
