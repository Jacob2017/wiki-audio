package id3

import "github.com/bogem/id3v2/v2"

// Tags is the set of ID3v2 frames wiki-audio writes onto each MP3.
// Fleshed out in Phase D — see wa-4cw.4.
type Tags struct {
	Title  string
	Artist string
	Album  string
	Date   string
}

// Write applies tags to the MP3 at path. Stub: not yet implemented.
func Write(path string, t Tags) error {
	_ = id3v2.Options{}
	return errStr("id3.Write: not yet implemented")
}

type errStr string

func (e errStr) Error() string { return string(e) }
