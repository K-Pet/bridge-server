package tagwriter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrFFmpegMissing is returned when the ffmpeg binary isn't on PATH.
// The runtime container always has it (Dockerfile installs the alpine
// `ffmpeg` package), so this is mainly a developer-machine concern.
// The handler surfaces it as 503 with a clear remediation message.
var ErrFFmpegMissing = errors.New("ffmpeg binary not found on PATH")

// writeViaFFmpeg rewrites container metadata without re-encoding the
// audio (`-c copy`). Used for formats where we don't ship a pure-Go
// writer: OGG Vorbis, Opus, and the MP4 family (m4a / aac / alac).
//
// ffmpeg normalizes our generic key names (title, artist, album_artist,
// date, track, disc, genre) to the container-appropriate tag keys
// (e.g. TITLE/ARTIST for Vorbis comments, ©nam/©ART for MP4 ilst).
// Empty values clear the tag.
//
// Crash safety: ffmpeg writes to a sibling temp file with the same
// extension (so it can pick the right muxer), then we rename over the
// original. A failure mid-encode leaves the temp file orphaned and
// the original intact.
func writeViaFFmpeg(path string, t Tags) error {
	ext := strings.ToLower(filepath.Ext(path))
	tmp := path + ".tag.tmp" + ext

	args := []string{
		"-y",
		"-loglevel", "error",
		"-i", path,
		"-map", "0",          // include every stream from input (audio + embedded art)
		"-map_metadata", "0", // start from input metadata, then override below
		"-c", "copy",         // no re-encode — bit-identical audio
	}

	add := func(key, val string) {
		args = append(args, "-metadata", key+"="+val)
	}
	intOrClear := func(key string, n int) {
		if n > 0 {
			add(key, strconv.Itoa(n))
		} else {
			add(key, "")
		}
	}
	if t.Title != nil {
		add("title", *t.Title)
	}
	if t.Artist != nil {
		add("artist", *t.Artist)
	}
	if t.AlbumArtist != nil {
		add("album_artist", *t.AlbumArtist)
	}
	if t.Album != nil {
		add("album", *t.Album)
	}
	if t.Year != nil {
		intOrClear("date", *t.Year)
	}
	if t.TrackNumber != nil {
		intOrClear("track", *t.TrackNumber)
	}
	if t.DiscNumber != nil {
		intOrClear("disc", *t.DiscNumber)
	}
	if t.Genre != nil {
		add("genre", *t.Genre)
	}

	args = append(args, tmp)

	// 60-second cap is generous: ffmpeg with `-c copy` finishes in well
	// under a second for any normal track, even hour-long mixes.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		var execErr *exec.Error
		if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
			return ErrFFmpegMissing
		}
		return fmt.Errorf("ffmpeg failed: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename ffmpeg output over original: %w", err)
	}
	return nil
}
