// Package library implements the user-driven music import flow:
// staging uploaded files in a per-session directory, reading their
// metadata, planning a normalized destination layout under the music
// root, and committing the staged files into the live library so
// Navidrome can index them on its next scan.
package library

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ImportRoot is the music-dir-relative folder that user-imported
// content lives under. Sibling of "Bridge/" (which the downloader
// uses for marketplace purchases). Keeping the two trees separate
// makes it easy to identify each source for backup or removal,
// while Navidrome still scans both together.
const ImportRoot = "Imported"

// StagingRoot is the music-dir-relative folder used to hold uploads
// in flight. Dot-prefixed so Navidrome ignores it during scans.
const StagingRoot = ".import-staging"

// SupportedExtensions is the closed set of audio file extensions the
// importer accepts. Anything else is rejected at upload time. Keep
// this list in sync with the Navidrome formats the marketplace
// advertises so users don't end up with files Navidrome can't index.
var SupportedExtensions = map[string]bool{
	".mp3":  true,
	".flac": true,
	".m4a":  true,
	".aac":  true,
	".ogg":  true,
	".oga":  true,
	".opus": true,
	".wav":  true,
	".aiff": true,
	".aif":  true,
	".wma":  true,
	".alac": true,
}

// TrackTags is the normalized subset of metadata the planner needs
// to decide where a file lives on disk. Built from whatever the tag
// library returns (or the fallback filename when tags are absent).
type TrackTags struct {
	Title       string
	Artist      string
	AlbumArtist string // preferred over Artist for grouping compilations
	Album       string
	TrackNumber int
	DiscNumber  int
	DiscTotal   int
	Extension   string // includes leading dot, lowercase (".flac")
}

// Plan is the planner's verdict for a single uploaded file: where it
// should be written, and what (if anything) about the input was
// missing or had to be substituted. Quality flags let the frontend
// surface "this file is missing an album tag" before the user
// commits — they can fix tags and re-upload, or accept the fallback.
type Plan struct {
	// RelPath is the destination path *relative to the music root*
	// (e.g. "Imported/Artist/Album/01 - Title.flac"). Callers join
	// with cfg.MusicDir to get the absolute host path.
	RelPath string

	// Effective tags used to build RelPath, after fallbacks and
	// sanitization. Useful so the UI shows the user what we *will*
	// write, not what was on the file.
	Effective TrackTags

	// Quality flags — frontend uses these to surface a "review needed"
	// state before commit.
	MissingTitle       bool
	MissingArtist      bool
	MissingAlbum       bool
	MissingTrackNumber bool
}

// PlanDestination computes the canonical relative destination path
// for a track given its (already-normalized) tags. The layout is:
//
//	Imported/<AlbumArtist>/<Album>/<TrackPrefix> - <Title><Ext>
//
// where TrackPrefix is "<disc>-<track>" when a multi-disc album is
// detected, "<track>" when a track number alone is present (zero-
// padded to two digits), or omitted entirely when neither is known.
//
// Missing tags fall through to "Unknown Artist" / "Unknown Album"
// rather than failing — the caller surfaces those substitutions via
// the Plan.Missing* flags so the user can fix tags and re-import if
// they care, or accept the fallback if they don't.
func PlanDestination(tags TrackTags) Plan {
	plan := Plan{Effective: tags}

	artist := strings.TrimSpace(tags.AlbumArtist)
	if artist == "" {
		artist = strings.TrimSpace(tags.Artist)
	}
	if artist == "" {
		artist = "Unknown Artist"
		plan.MissingArtist = true
	}

	album := strings.TrimSpace(tags.Album)
	if album == "" {
		album = "Unknown Album"
		plan.MissingAlbum = true
	}

	title := strings.TrimSpace(tags.Title)
	if title == "" {
		// Strip extension off whatever filename hint we got and use
		// that — better a recognisable "track01" than "Unknown Title".
		title = "Unknown Title"
		plan.MissingTitle = true
	}

	ext := strings.ToLower(strings.TrimSpace(tags.Extension))
	if ext == "" {
		ext = ".bin" // shouldn't happen — upload handler rejects unknown types
	}

	// Build the filename prefix. Multi-disc albums (DiscTotal > 1) get
	// "<disc>-<track>" so the natural sort across discs interleaves
	// correctly; single-disc gets just "<track>".
	prefix := ""
	switch {
	case tags.TrackNumber > 0 && tags.DiscTotal > 1:
		prefix = fmt.Sprintf("%d-%02d ", tags.DiscNumber, tags.TrackNumber)
	case tags.TrackNumber > 0:
		prefix = fmt.Sprintf("%02d ", tags.TrackNumber)
	default:
		plan.MissingTrackNumber = true
	}

	filename := strings.TrimSpace(prefix) + " - " + title + ext
	if prefix == "" {
		// No track prefix — drop the leading " - " too, otherwise we'd
		// get filenames like " - Title.flac".
		filename = title + ext
	}

	plan.RelPath = filepath.Join(
		ImportRoot,
		Sanitize(artist),
		Sanitize(album),
		Sanitize(filename),
	)

	plan.Effective.Title = title
	plan.Effective.Artist = artist
	plan.Effective.AlbumArtist = artist
	plan.Effective.Album = album
	plan.Effective.Extension = ext

	return plan
}

// unsafePathChars matches characters that are illegal or dangerous in
// path segments on at least one major filesystem (Windows reserves
// `<>:"/\|?*`, NTFS forbids control chars, every fs forbids `/`). We
// also strip NULs and the path separator itself.
var unsafePathChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// Sanitize cleans a single path segment so it is safe to write to
// disk on macOS, Linux, and Windows-mounted volumes (some users
// run Navidrome over SMB/NTFS for shared storage). Replaces unsafe
// characters with `_`, trims surrounding dots and whitespace, and
// caps the result so we don't blow past the per-component name
// limit (255 bytes on most filesystems; we cap at 200 to leave
// room for prefixes a caller might prepend).
//
// Empty input returns "Unknown" so callers always get a usable
// segment back without having to special-case the zero value.
func Sanitize(name string) string {
	name = strings.TrimSpace(name)
	name = unsafePathChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, ". ")
	if name == "" {
		return "Unknown"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

// IsSupportedAudio reports whether ext (with leading dot, any case)
// is in the SupportedExtensions allowlist.
func IsSupportedAudio(ext string) bool {
	return SupportedExtensions[strings.ToLower(ext)]
}
