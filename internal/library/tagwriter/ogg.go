package tagwriter

import "fmt"

// writeOGG is a stub for OGG Vorbis / Opus. The container wraps
// Vorbis comments inside Ogg pages, so writing means re-paginating
// the stream after the comment-block edit — non-trivial enough to
// defer to a follow-up PR. Returns ErrUnsupportedFormat so the
// handler emits a clean 415.
func writeOGG(path string, t Tags) error {
	return fmt.Errorf("%w: ogg/opus editing not yet implemented", ErrUnsupportedFormat)
}
