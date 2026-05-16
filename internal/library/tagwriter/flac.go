package tagwriter

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-flac/flacvorbis/v2"
	flac "github.com/go-flac/go-flac/v2"
)

// writeFLAC updates Vorbis comments inside a FLAC file. go-flac
// reads the entire file into memory, lets us mutate the metadata
// blocks, then rewrites the whole thing on Save. We write to a
// sibling temp path and rename over the original so a crash mid-
// write can't leave a half-rewritten audio file behind.
func writeFLAC(path string, t Tags) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse flac: %w", err)
	}

	cmt, cmtIdx, err := findVorbisComment(f)
	if err != nil {
		return err
	}

	if t.Title != nil {
		setVorbisField(cmt, "TITLE", *t.Title)
	}
	if t.Artist != nil {
		setVorbisField(cmt, "ARTIST", *t.Artist)
	}
	if t.AlbumArtist != nil {
		setVorbisField(cmt, "ALBUMARTIST", *t.AlbumArtist)
	}
	if t.Album != nil {
		setVorbisField(cmt, "ALBUM", *t.Album)
	}
	if t.Year != nil {
		if *t.Year > 0 {
			setVorbisField(cmt, "DATE", strconv.Itoa(*t.Year))
		} else {
			setVorbisField(cmt, "DATE", "")
		}
	}
	if t.TrackNumber != nil {
		if *t.TrackNumber > 0 {
			setVorbisField(cmt, "TRACKNUMBER", strconv.Itoa(*t.TrackNumber))
		} else {
			setVorbisField(cmt, "TRACKNUMBER", "")
		}
	}
	if t.DiscNumber != nil {
		if *t.DiscNumber > 0 {
			setVorbisField(cmt, "DISCNUMBER", strconv.Itoa(*t.DiscNumber))
		} else {
			setVorbisField(cmt, "DISCNUMBER", "")
		}
	}
	if t.Genre != nil {
		setVorbisField(cmt, "GENRE", *t.Genre)
	}

	// Re-marshal the comment block back into place. If the file had
	// no VorbisComment block to begin with, append a fresh one.
	marshaled := cmt.Marshal()
	if cmtIdx >= 0 {
		f.Meta[cmtIdx] = &marshaled
	} else {
		f.Meta = append(f.Meta, &marshaled)
	}

	// Atomic write: same directory, same filesystem, then rename.
	tmp := path + ".tag.tmp"
	if err := f.Save(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("save flac tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename flac tmp over original: %w", err)
	}
	return nil
}

// findVorbisComment returns the parsed VorbisComment block plus its
// index in f.Meta so the caller can replace it after editing. Index
// is -1 when no VorbisComment block exists — caller should append a
// new one in that case.
func findVorbisComment(f *flac.File) (*flacvorbis.MetaDataBlockVorbisComment, int, error) {
	for i, block := range f.Meta {
		if block.Type != flac.VorbisComment {
			continue
		}
		cmt, err := flacvorbis.ParseFromMetaDataBlock(*block)
		if err != nil {
			return nil, -1, fmt.Errorf("parse vorbis comment: %w", err)
		}
		return cmt, i, nil
	}
	return flacvorbis.New(), -1, nil
}

// setVorbisField rewrites a Vorbis comment field. Vorbis allows
// repeated keys; we treat single-valued fields as such and replace
// all existing entries with one new value (or none, if val is empty).
// Field names are stored uppercase per the Vorbis convention.
func setVorbisField(cmt *flacvorbis.MetaDataBlockVorbisComment, key, val string) {
	key = strings.ToUpper(key)
	filtered := cmt.Comments[:0]
	for _, entry := range cmt.Comments {
		eq := strings.IndexByte(entry, '=')
		if eq < 0 {
			filtered = append(filtered, entry)
			continue
		}
		if !strings.EqualFold(entry[:eq], key) {
			filtered = append(filtered, entry)
		}
	}
	cmt.Comments = filtered
	if val != "" {
		cmt.Comments = append(cmt.Comments, key+"="+val)
	}
}
