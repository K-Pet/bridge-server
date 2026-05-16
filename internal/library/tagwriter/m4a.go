package tagwriter

import "fmt"

// writeM4A is a stub. MP4 container tag editing (ilst atoms inside
// moov.udta.meta) is non-trivial: a robust implementation needs to
// rebuild the moov atom and patch stco/co64 offsets. Punted to a
// follow-up PR — for now we return ErrUnsupportedFormat so the
// handler can return a structured 415 rather than crashing.
func writeM4A(path string, t Tags) error {
	return fmt.Errorf("%w: m4a/aac/alac editing not yet implemented", ErrUnsupportedFormat)
}
