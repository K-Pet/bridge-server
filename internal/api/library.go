package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/library/autotag"
	"github.com/bridgemusic/bridge-server/internal/library/tagwriter"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/store"
)

// maxCoverBytes caps the album-cover upload size. Album art rarely
// exceeds 5 MB even at 3000x3000 PNG; 10 MB is generous headroom for
// the rare hi-res master without letting arbitrary uploads chew disk.
const maxCoverBytes = 10 << 20

// handleDeleteSong removes a song file from the music library and purges
// its reference from Navidrome's database. The full flow:
//  1. Look up the song's file path via the Subsonic API
//  2. Delete the file from disk (synchronous — real error to frontend)
//  3. Trigger a Navidrome scan + purge in the background
//  4. Publish a library_updated SSE event
func handleDeleteSong(cfg *config.Config, nd *navidrome.Client, queue *store.Queue, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		songID := r.PathValue("id")
		if songID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Song ID is required.")
			return
		}

		// Look up the song's file path from Navidrome
		song, err := nd.GetSong(r.Context(), songID)
		if err != nil {
			slog.Warn("delete song: lookup failed", "song", songID, "error", err)
			writeJSONError(w, http.StatusNotFound, "not_found", "Song not found in library.")
			return
		}

		slog.Info("deleting song", "song", songID, "title", song.Title, "path", song.Path, "user", userID)

		// Delete the file synchronously so the frontend gets a real error
		// if the file can't be removed.
		hostPath := nd.HostPath(song.Path)
		if err := navidrome.DeleteSongFile(hostPath); err != nil {
			slog.Error("delete song: file removal failed", "song", songID, "hostPath", hostPath, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "delete_failed",
				"Failed to delete file from disk: "+err.Error())
			return
		}

		slog.Info("delete song: file removed", "song", songID, "hostPath", hostPath)

		// Clear any matching task rows so the webhook idempotency guard
		// no longer reports this purchase as fully delivered. Without
		// this, a subsequent marketplace "redeliver" would be short-
		// circuited by the guard and the purchase would stall in
		// `delivering` forever.
		if queue != nil {
			if removed, err := queue.DeleteTasksAtPaths(cfg.MusicDir, []string{hostPath}); err != nil {
				slog.Warn("delete song: clearing matching tasks failed", "hostPath", hostPath, "error", err)
			} else if removed > 0 {
				slog.Info("delete song: cleared tasks", "hostPath", hostPath, "removed", removed)
			}
		}

		// Scan + purge in background — the file is already gone.
		go func() {
			ctx := context.Background()
			if err := nd.ScanAndPurge(ctx); err != nil {
				slog.Error("delete song: scan+purge failed", "song", songID, "error", err)
			}
			if hub != nil {
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{"deleted_songs": []string{songID}},
				})
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"deleted":  true,
			"song_id":  songID,
			"scanning": true,
		})
	}
}

// songTagsRequest is the JSON body of PUT /api/library/songs/{id}.
// All fields use pointers so callers can send a partial patch — only
// non-nil fields are written to the file. An empty string clears a
// field; a missing key leaves it alone.
type songTagsRequest struct {
	Title       *string `json:"title,omitempty"`
	Artist      *string `json:"artist,omitempty"`
	AlbumArtist *string `json:"album_artist,omitempty"`
	Album       *string `json:"album,omitempty"`
	Year        *int    `json:"year,omitempty"`
	TrackNumber *int    `json:"track_number,omitempty"`
	DiscNumber  *int    `json:"disc_number,omitempty"`
	Genre       *string `json:"genre,omitempty"`
}

func (r songTagsRequest) toTags() tagwriter.Tags {
	return tagwriter.Tags{
		Title:       r.Title,
		Artist:      r.Artist,
		AlbumArtist: r.AlbumArtist,
		Album:       r.Album,
		Year:        r.Year,
		TrackNumber: r.TrackNumber,
		DiscNumber:  r.DiscNumber,
		Genre:       r.Genre,
	}
}

// handleUpdateSongTags writes edited metadata to the audio file on
// disk and then triggers a Navidrome rescan. Navidrome owns the
// database view of tags but treats files as the source of truth, so
// the only way to persist a user edit is to mutate the file and let
// Navidrome reindex it.
//
// Flow:
//  1. Resolve the file path from Navidrome's native API
//  2. Reject unsupported formats up front (e.g. m4a, ogg) with 415
//  3. Write tags to the file (atomic via the tagwriter package)
//  4. Trigger a Navidrome scan in the background
//  5. Publish a library_updated SSE event so the SPA refreshes
func handleUpdateSongTags(_ *config.Config, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		songID := r.PathValue("id")
		if songID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Song ID is required.")
			return
		}

		var req songTagsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_json", "Invalid JSON body: "+err.Error())
			return
		}
		tags := req.toTags()
		if !tags.HasChanges() {
			writeJSONError(w, http.StatusBadRequest, "no_changes", "Request did not set any tag fields.")
			return
		}

		song, err := nd.GetSong(r.Context(), songID)
		if err != nil {
			slog.Warn("update song tags: lookup failed", "song", songID, "error", err)
			writeJSONError(w, http.StatusNotFound, "not_found", "Song not found in library.")
			return
		}
		if song.Path == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_path", "Navidrome did not return a file path for this song.")
			return
		}

		ext := strings.ToLower(filepath.Ext(song.Path))
		if !tagwriter.SupportsWrite(ext) {
			writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_format",
				"Tag editing is not yet supported for "+ext+" files.")
			return
		}

		hostPath := nd.HostPath(song.Path)
		slog.Info("updating song tags", "song", songID, "hostPath", hostPath, "user", userID)

		if err := tagwriter.WriteTags(hostPath, tags); err != nil {
			if errors.Is(err, tagwriter.ErrUnsupportedFormat) {
				writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_format", err.Error())
				return
			}
			if errors.Is(err, tagwriter.ErrFFmpegMissing) {
				writeJSONError(w, http.StatusServiceUnavailable, "ffmpeg_missing",
					"The ffmpeg binary is not installed on the server (required for OGG/Opus/M4A edits).")
				return
			}
			slog.Error("update song tags: write failed", "song", songID, "hostPath", hostPath, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "write_failed",
				"Failed to write tags to file: "+err.Error())
			return
		}

		// Scan in the background so the request returns quickly. The
		// SSE event tells the SPA when the new metadata is queryable.
		go func() {
			ctx := context.Background()
			if err := nd.StartScan(ctx); err != nil {
				slog.Error("update song tags: scan trigger failed", "song", songID, "error", err)
			}
			if hub != nil {
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{"updated_song": songID},
				})
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"updated":  true,
			"song_id":  songID,
			"scanning": true,
		})
	}
}

// handleIdentifySong runs Chromaprint + AcoustID + MusicBrainz
// against a track's audio file and returns up to N candidate matches
// for the UI to display. Nothing is written; the user picks a
// candidate and the existing PUT /api/library/songs/{id} endpoint
// applies the chosen values.
//
// Returns 503 when fpcalc isn't installed or when the operator hasn't
// set BRIDGE_ACOUSTID_KEY — both are environmental concerns and the
// frontend uses the structured error code to hide the affordance
// rather than retry.
func handleIdentifySong(cfg *config.Config, nd *navidrome.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		songID := r.PathValue("id")
		if songID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Song ID is required.")
			return
		}

		if cfg.AcoustIDKey == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "acoustid_not_configured",
				"Auto-identification is not configured on this server (BRIDGE_ACOUSTID_KEY unset).")
			return
		}

		song, err := nd.GetSong(r.Context(), songID)
		if err != nil {
			slog.Warn("identify song: lookup failed", "song", songID, "error", err)
			writeJSONError(w, http.StatusNotFound, "not_found", "Song not found in library.")
			return
		}
		if song.Path == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_path", "Navidrome did not return a file path for this song.")
			return
		}

		hostPath := nd.HostPath(song.Path)
		slog.Info("identifying song", "song", songID, "hostPath", hostPath, "user", userID)

		client := autotag.New(cfg.AcoustIDKey, "bridge-server/1.0 (+https://bridgemusic.app)")
		candidates, err := client.Identify(r.Context(), hostPath, 5)
		if err != nil {
			if errors.Is(err, autotag.ErrFingerprinterMissing) {
				writeJSONError(w, http.StatusServiceUnavailable, "fpcalc_missing",
					"The fpcalc binary (libchromaprint-tools) is not installed on the server.")
				return
			}
			if errors.Is(err, autotag.ErrNoMatches) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"song_id":    songID,
					"candidates": []autotag.Candidate{},
				})
				return
			}
			slog.Error("identify song: failed", "song", songID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "identify_failed",
				"Failed to identify track: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"song_id":    songID,
			"candidates": candidates,
		})
	}
}

// handleUploadAlbumCover replaces the folder-level cover art for an
// album. Navidrome scans for `cover.{jpg,png}`, `folder.*`, `front.*`,
// and `album.*` files alongside the audio; we standardize on
// `cover.jpg` or `cover.png` and strip the alternates so Navidrome
// can't pick stale variants on the next scan.
//
// The handler accepts a raw PUT body with the appropriate Content-Type
// (image/jpeg or image/png) — multipart adds little here, since we
// only ever carry one file per request.
func handleUploadAlbumCover(_ *config.Config, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		albumID := r.PathValue("id")
		if albumID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Album ID is required.")
			return
		}

		ext, err := coverExtFromContentType(r.Header.Get("Content-Type"))
		if err != nil {
			writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_format", err.Error())
			return
		}

		// Use the first song's parent directory as the album folder.
		// All songs in a Navidrome album share a directory by
		// convention (the import pipeline and downloader both lay
		// files out that way).
		songs, err := nd.GetAlbumSongs(r.Context(), albumID)
		if err != nil {
			slog.Warn("upload album cover: lookup failed", "album", albumID, "error", err)
			writeJSONError(w, http.StatusNotFound, "not_found", "Album not found in library.")
			return
		}
		if len(songs.Songs) == 0 || songs.Songs[0].Path == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_path", "Album has no songs with a known file path.")
			return
		}
		albumDir := filepath.Dir(nd.HostPath(songs.Songs[0].Path))

		// Stream into a temp file in the same directory so the final
		// rename is atomic and on the same filesystem.
		tmp := filepath.Join(albumDir, ".cover.tmp"+ext)
		f, err := os.Create(tmp)
		if err != nil {
			slog.Error("upload album cover: create tmp", "album", albumID, "path", tmp, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "create_tmp", "Failed to create temp file: "+err.Error())
			return
		}
		written, copyErr := io.Copy(f, io.LimitReader(http.MaxBytesReader(w, r.Body, maxCoverBytes), maxCoverBytes+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			os.Remove(tmp)
			cause := copyErr
			if cause == nil {
				cause = closeErr
			}
			slog.Warn("upload album cover: write tmp", "album", albumID, "error", cause)
			writeJSONError(w, http.StatusBadRequest, "write_failed", "Failed to read upload: "+cause.Error())
			return
		}
		if written == 0 {
			os.Remove(tmp)
			writeJSONError(w, http.StatusBadRequest, "empty_body", "Request body was empty.")
			return
		}

		finalPath := filepath.Join(albumDir, "cover"+ext)
		if err := os.Rename(tmp, finalPath); err != nil {
			os.Remove(tmp)
			slog.Error("upload album cover: rename", "album", albumID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "rename_failed", "Failed to install cover: "+err.Error())
			return
		}
		// Remove competing cover variants so Navidrome doesn't pick a
		// stale image on rescan (preference order is folder.* then
		// front.* then album.* then cover.* in some configs — keeping
		// only one canonical file removes the ambiguity).
		removeCoverAlternates(albumDir, filepath.Base(finalPath))

		slog.Info("upload album cover: installed", "album", albumID, "path", finalPath, "bytes", written, "user", userID)

		go func() {
			ctx := context.Background()
			if err := nd.StartScan(ctx); err != nil {
				slog.Error("upload album cover: scan trigger failed", "album", albumID, "error", err)
			}
			if hub != nil {
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{"updated_album_cover": albumID},
				})
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"updated":  true,
			"album_id": albumID,
			"bytes":    written,
			"scanning": true,
		})
	}
}

// coverExtFromContentType maps an Accept-style image MIME to a file
// extension. Restricted to the two formats Navidrome handles natively
// — keeping the allowlist tight means we never accept a bogus
// container that would only confuse Navidrome's scanner.
func coverExtFromContentType(ct string) (string, error) {
	// Strip any charset/boundary suffix and normalize case.
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/jpeg", "image/jpg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	default:
		return "", fmt.Errorf("unsupported cover image type %q (must be image/jpeg or image/png)", ct)
	}
}

// removeCoverAlternates deletes the other folder-level cover files in
// dir, keeping only the file named `keep`. Navidrome picks any of
// these on scan, so leaving stale variants behind would let an old
// upload re-surface.
func removeCoverAlternates(dir, keep string) {
	keep = strings.ToLower(keep)
	candidates := []string{
		"cover.jpg", "cover.jpeg", "cover.png",
		"folder.jpg", "folder.jpeg", "folder.png",
		"front.jpg", "front.jpeg", "front.png",
		"album.jpg", "album.jpeg", "album.png",
	}
	for _, name := range candidates {
		if name == keep {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// handleDeleteAlbum removes all songs in an album from the music library
// and purges their references from Navidrome's database.
func handleDeleteAlbum(cfg *config.Config, nd *navidrome.Client, queue *store.Queue, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		albumID := r.PathValue("id")
		if albumID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Album ID is required.")
			return
		}

		// Look up all songs in the album
		albumSongs, err := nd.GetAlbumSongs(r.Context(), albumID)
		if err != nil {
			slog.Warn("delete album: lookup failed", "album", albumID, "error", err)
			writeJSONError(w, http.StatusNotFound, "not_found", "Album not found in library.")
			return
		}

		if len(albumSongs.Songs) == 0 {
			writeJSONError(w, http.StatusNotFound, "empty_album", "Album has no songs.")
			return
		}

		slog.Info("deleting album", "album", albumID, "songs", len(albumSongs.Songs), "user", userID)

		// Collect host paths up front so we can purge matching task rows
		// after the on-disk delete succeeds. Keeps the webhook idempotency
		// guard honest when the marketplace triggers a redelivery.
		hostPaths := make([]string, 0, len(albumSongs.Songs))
		for _, s := range albumSongs.Songs {
			if s.Path == "" {
				continue
			}
			hostPaths = append(hostPaths, nd.HostPath(s.Path))
		}

		// Delete files synchronously so the frontend gets a real error.
		removed, err := nd.DeleteSongFiles(albumSongs.Songs)
		if err != nil {
			slog.Error("delete album: file removal failed", "album", albumID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "delete_failed",
				"Failed to delete files from disk: "+err.Error())
			return
		}

		slog.Info("delete album: files removed", "album", albumID, "removed", removed)

		if queue != nil && len(hostPaths) > 0 {
			if rmTasks, err := queue.DeleteTasksAtPaths(cfg.MusicDir, hostPaths); err != nil {
				slog.Warn("delete album: clearing matching tasks failed", "album", albumID, "error", err)
			} else if rmTasks > 0 {
				slog.Info("delete album: cleared tasks", "album", albumID, "removed", rmTasks)
			}
		}

		// Scan + purge in background — files are already gone.
		go func() {
			ctx := context.Background()
			if err := nd.ScanAndPurge(ctx); err != nil {
				slog.Error("delete album: scan+purge failed", "album", albumID, "error", err)
			}
			if hub != nil {
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{"deleted_album": albumID},
				})
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"deleted":    true,
			"album_id":   albumID,
			"song_count": len(albumSongs.Songs),
			"scanning":   true,
		})
	}
}
