package tagwriter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// writeMP3 updates ID3v2 frames on an MP3 file in place. bogem/id3v2
// writes via a temp file in the same directory and renames atomically
// over the original, so we don't repeat that dance ourselves.
//
// Defaults to ID3v2.4 with UTF-8 — the modern standard. If the file
// already has v2.3 tags they're upgraded to v2.4 on save, which
// every modern player (including Navidrome's underlying tag parser)
// handles fine.
//
// bogem only supports v2.3 and v2.4. Files with the older v2.2 layout
// (common in iTunes-era MP3s) trip its "unsupported version" error;
// we fall back to ffmpeg in that case, which transparently rewrites
// the tag header as v2.4 while copying the audio frames untouched.
func writeMP3(path string, t Tags) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		if isUnsupportedID3Version(err) {
			return writeViaFFmpeg(path, t)
		}
		return fmt.Errorf("open mp3: %w", err)
	}
	defer tag.Close()

	// Force UTF-8 on all new text frames so we don't end up with a
	// mix of Latin-1 (the v2.3 default) and UTF-8 in the same file.
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)

	if t.Title != nil {
		tag.SetTitle(*t.Title)
	}
	if t.Artist != nil {
		tag.SetArtist(*t.Artist)
	}
	if t.AlbumArtist != nil {
		// bogem doesn't have a typed setter for TPE2; add the frame
		// directly. We delete any existing TPE2 first to avoid
		// stacking duplicate frames across edits.
		tag.DeleteFrames("TPE2")
		if *t.AlbumArtist != "" {
			tag.AddTextFrame("TPE2", tag.DefaultEncoding(), *t.AlbumArtist)
		}
	}
	if t.Album != nil {
		tag.SetAlbum(*t.Album)
	}
	if t.Year != nil {
		if *t.Year > 0 {
			tag.SetYear(strconv.Itoa(*t.Year))
		} else {
			tag.SetYear("")
		}
	}
	if t.TrackNumber != nil {
		tag.DeleteFrames("TRCK")
		if *t.TrackNumber > 0 {
			tag.AddTextFrame("TRCK", tag.DefaultEncoding(), strconv.Itoa(*t.TrackNumber))
		}
	}
	if t.DiscNumber != nil {
		tag.DeleteFrames("TPOS")
		if *t.DiscNumber > 0 {
			tag.AddTextFrame("TPOS", tag.DefaultEncoding(), strconv.Itoa(*t.DiscNumber))
		}
	}
	if t.Genre != nil {
		tag.SetGenre(*t.Genre)
	}

	if err := tag.Save(); err != nil {
		return fmt.Errorf("save mp3 tags: %w", err)
	}
	return nil
}

// isUnsupportedID3Version detects the specific bogem/id3v2 error for
// pre-v2.3 (v2.2) tags. The library doesn't export a typed error for
// this, so we string-match — fragile, but the message has been stable
// across releases and the alternative is parsing the file header
// ourselves before every open.
func isUnsupportedID3Version(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unsupported version of ID3 tag")
}
