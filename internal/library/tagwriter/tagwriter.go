// Package tagwriter writes editable metadata back to audio files on
// disk. Navidrome treats files as the source of truth for tags and
// does not expose tag-write APIs, so any user edit must round-trip
// through the file: we mutate the tag frames, then trigger a
// Navidrome rescan to refresh its DB.
//
// The package dispatches by extension and delegates to a format-
// specific writer. Each writer is responsible for crash-safety —
// either by using a library that writes via temp-file + rename, or
// by doing the same dance explicitly. Callers should pass the host
// filesystem path (translated from Navidrome's library-relative path
// via navidrome.Client.HostPath).
package tagwriter

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Tags holds the fields the editor exposes. Pointer fields use the
// usual three-state convention: nil leaves the existing value alone,
// a non-nil pointer to "" (or 0) clears the field, a non-nil pointer
// to a value sets it. This lets callers send a partial JSON patch.
type Tags struct {
	Title       *string
	Artist      *string
	AlbumArtist *string
	Album       *string
	Year        *int
	TrackNumber *int
	DiscNumber  *int
	Genre       *string
}

// HasChanges reports whether at least one field is non-nil. Used by
// the HTTP handler to short-circuit a no-op request before touching
// the filesystem.
func (t Tags) HasChanges() bool {
	return t.Title != nil || t.Artist != nil || t.AlbumArtist != nil ||
		t.Album != nil || t.Year != nil || t.TrackNumber != nil ||
		t.DiscNumber != nil || t.Genre != nil
}

// ErrUnsupportedFormat is returned when the file's extension has no
// writer registered. The handler maps this to a 415 so the UI can
// tell the user "we can't edit this format" instead of presenting a
// generic failure.
var ErrUnsupportedFormat = errors.New("tag editing not supported for this format")

// WriteTags applies tags to the file at hostPath. Dispatches by
// extension (lowercased). The path must point to a real file on disk
// — symlinks are followed by the underlying libraries.
func WriteTags(hostPath string, tags Tags) error {
	if !tags.HasChanges() {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(hostPath))
	switch ext {
	case ".mp3":
		return writeMP3(hostPath, tags)
	case ".flac":
		return writeFLAC(hostPath, tags)
	case ".m4a", ".aac", ".alac",
		".ogg", ".oga", ".opus":
		return writeViaFFmpeg(hostPath, tags)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

// SupportsWrite reports whether the file extension has a writer
// implementation (not a stub). The handler uses this for upfront
// validation so the UI can disable the edit affordance for files
// we can't actually modify.
//
// MP3 and FLAC use format-specific Go libraries; OGG, Opus, and the
// MP4 family route through ffmpeg with -c copy (no re-encoding).
func SupportsWrite(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp3", ".flac",
		".ogg", ".oga", ".opus",
		".m4a", ".aac", ".alac":
		return true
	default:
		return false
	}
}
