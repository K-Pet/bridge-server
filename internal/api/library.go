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
	"time"

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

// applyTagsToSongs writes the given tag patch to each song file in
// parallel-safe sequence and triggers exactly one Navidrome rescan at
// the end. Returns the IDs that succeeded and the IDs that failed
// (with the per-song write error logged at warn level so a partial
// failure doesn't kill the whole batch).
//
// Used by the album- and artist-level edit endpoints, both of which
// fan a single user patch across a song collection.
func applyTagsToSongs(ctx context.Context, songs []navidrome.SongInfo, tags tagwriter.Tags, nd *navidrome.Client, hub *EventHub, scope string, scopeID string) (updated []string, failed []string) {
	for _, s := range songs {
		if s.Path == "" {
			failed = append(failed, s.ID)
			continue
		}
		ext := strings.ToLower(filepath.Ext(s.Path))
		if !tagwriter.SupportsWrite(ext) {
			failed = append(failed, s.ID)
			continue
		}
		hostPath := nd.HostPath(s.Path)
		if err := tagwriter.WriteTags(hostPath, tags); err != nil {
			slog.Warn("batch tag write failed", "song", s.ID, "hostPath", hostPath, "error", err)
			failed = append(failed, s.ID)
			continue
		}
		updated = append(updated, s.ID)
	}

	if len(updated) > 0 {
		go func() {
			scanCtx := context.Background()
			if err := nd.StartScan(scanCtx); err != nil {
				slog.Error("batch tag write: scan trigger failed", "scope", scope, "id", scopeID, "error", err)
			}
			if hub != nil {
				// "complete:true" + per-scope counts let the SPA replace
				// a "Saving…" toast with a "Saved N of M tracks" summary
				// after the rescan settles.
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{
						"operation":           scope + "_edit",
						"complete":            true,
						"updated_" + scope:    scopeID,
						"updated_track_count": len(updated),
						"failed_count":        len(failed),
					},
				})
			}
		}()
	}
	_ = ctx
	return updated, failed
}

// albumTagsRequest holds the subset of tag fields it makes sense to
// edit at the album level. Per-track fields (title, track_number) are
// intentionally absent — they're edited via the song endpoint. Artist
// is also excluded; renaming the performing artist on every track of
// an album would usually want to be an artist-scoped operation, not
// album-scoped.
type albumTagsRequest struct {
	AlbumArtist *string `json:"album_artist,omitempty"`
	Album       *string `json:"album,omitempty"`
	Year        *int    `json:"year,omitempty"`
	Genre       *string `json:"genre,omitempty"`
}

func (r albumTagsRequest) toTags() tagwriter.Tags {
	return tagwriter.Tags{
		AlbumArtist: r.AlbumArtist,
		Album:       r.Album,
		Year:        r.Year,
		Genre:       r.Genre,
	}
}

func handleUpdateAlbumTags(_ *config.Config, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
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

		var req albumTagsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_json", "Invalid JSON body: "+err.Error())
			return
		}
		tags := req.toTags()
		if !tags.HasChanges() {
			writeJSONError(w, http.StatusBadRequest, "no_changes", "Request did not set any tag fields.")
			return
		}

		albumSongs, err := nd.GetAlbumSongs(r.Context(), albumID)
		if err != nil || albumSongs == nil || len(albumSongs.Songs) == 0 {
			writeJSONError(w, http.StatusNotFound, "not_found", "Album not found or empty.")
			return
		}

		slog.Info("updating album tags", "album", albumID, "songs", len(albumSongs.Songs), "user", userID)

		// Async fan-out, same rationale as handleRenameArtist: SD-card
		// I/O on low-end hardware can exceed Cloudflare's edge
		// timeout. The 202 lands fast; completion arrives via SSE.
		songsCopy := albumSongs.Songs
		go func() {
			applyTagsToSongs(context.Background(), songsCopy, tags, nd, hub, "album", albumID)
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted":     true,
			"album_id":     albumID,
			"songs_queued": len(albumSongs.Songs),
		})
	}
}

// renameArtistRequest is the body shape for the artist-rename
// endpoint. One field, deliberately: artist renames are a "rename this
// artist everywhere" operation, not a piecemeal patch of AlbumArtist
// vs Artist tags. The server figures out the cascade per-track.
type renameArtistRequest struct {
	NewName string `json:"new_name"`
}

// handleRenameArtist renames every track attributed to an artist with
// a smart cascade that preserves featured-artist credits.
//
// Rules:
//   - AlbumArtist (TPE2 / ALBUMARTIST) is rewritten on every track.
//     This is the grouping field — Navidrome uses it to bucket albums
//     under an artist — so it must move uniformly.
//   - Track Artist (TPE1 / ARTIST) is rewritten only when it exactly
//     matches the old artist name (case-insensitive, trimmed). Any
//     value containing a feature delimiter ("Drake feat. 21 Savage",
//     "Run-DMC & Aerosmith", "X, Y") stays untouched — that string is
//     a *credit*, not just an artist reference, and clobbering it
//     would corrupt the per-track credit metadata.
//
// Returns counts of how many tracks were fully renamed vs. left as
// features so the UI can surface the result honestly.
func handleRenameArtist(_ *config.Config, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		artistID := r.PathValue("id")
		if artistID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Artist ID is required.")
			return
		}

		var req renameArtistRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_json", "Invalid JSON body: "+err.Error())
			return
		}
		newName := strings.TrimSpace(req.NewName)
		if newName == "" {
			writeJSONError(w, http.StatusBadRequest, "empty_name", "New artist name is required.")
			return
		}

		songs, err := nd.GetArtistSongs(r.Context(), artistID)
		if err != nil || len(songs) == 0 {
			writeJSONError(w, http.StatusNotFound, "not_found", "Artist not found or has no songs.")
			return
		}

		// Derive the old name from the first song's AlbumArtist field
		// (falling back to Artist if AlbumArtist is empty — happens on
		// hastily-tagged imports). All songs in this artist_id group
		// share the same AlbumArtist by construction, so the first
		// one is canonical.
		oldName := strings.TrimSpace(songs[0].AlbumArtist)
		if oldName == "" {
			oldName = strings.TrimSpace(songs[0].Artist)
		}
		if oldName == "" {
			writeJSONError(w, http.StatusInternalServerError, "no_old_name",
				"Could not determine current artist name from Navidrome.")
			return
		}
		if strings.EqualFold(oldName, newName) {
			writeJSONError(w, http.StatusBadRequest, "no_changes",
				"New name matches the current name.")
			return
		}

		slog.Info("renaming artist", "artist", artistID, "old", oldName, "new", newName, "songs", len(songs), "user", userID)

		// Capture any uploaded artist photo BEFORE the rename so we
		// can carry it over to the new artist row. Navidrome's
		// uploaded_image column is keyed by artist id, and renaming
		// changes the id (it's a hash of the name) — without this
		// snapshot the user would lose their custom photo on every
		// rename.
		//
		// Done synchronously (before the goroutine spawns) so we read
		// the photo while the OLD artist row still exists. Capping
		// FetchCoverArt with the request context keeps a slow
		// Navidrome from holding the response forever; the rest of
		// the rename runs in the background regardless.
		var savedPhotoBytes []byte
		if details, err := nd.GetArtistDetails(r.Context(), artistID); err == nil && details != nil && details.UploadedImage != "" {
			if bytesData, ferr := nd.FetchCoverArt(r.Context(), "ar-"+artistID+"_0"); ferr == nil && len(bytesData) > 0 {
				savedPhotoBytes = bytesData
				slog.Info("rename artist: captured existing photo", "artist", artistID, "bytes", len(bytesData))
			} else if ferr != nil {
				slog.Warn("rename artist: failed to capture photo", "artist", artistID, "error", ferr)
			}
		}

		// Tag writes + scan + photo restore run async. On low-end
		// hardware (e.g. Pi Zero 2W writing dozens of FLACs to an SD
		// card) the sync version of this could push past Cloudflare's
		// 100 s edge timeout and 504 even though Navidrome is fine.
		// The client gets a 202 immediately and tracks completion via
		// the library_updated SSE event published when the goroutine
		// finishes — that event now carries the cascade counts so the
		// UI can show "Renamed 47, kept 8 features intact" without
		// the original PUT having to wait for the work.
		go func() {
			ctx := context.Background()
			var renamedTracks, preservedFeatures, failedTracks []string

			for _, s := range songs {
				if s.Path == "" {
					failedTracks = append(failedTracks, s.ID)
					continue
				}
				ext := strings.ToLower(filepath.Ext(s.Path))
				if !tagwriter.SupportsWrite(ext) {
					failedTracks = append(failedTracks, s.ID)
					continue
				}

				tags := tagwriter.Tags{
					AlbumArtist: &newName,
				}
				// Track Artist rename: only when the current value is
				// an exact match for the old artist. Anything else is
				// treated as a credit string (likely a feature) and
				// preserved.
				isExactSolo := strings.EqualFold(strings.TrimSpace(s.Artist), oldName)
				if isExactSolo {
					tags.Artist = &newName
				}

				hostPath := nd.HostPath(s.Path)
				if err := tagwriter.WriteTags(hostPath, tags); err != nil {
					slog.Warn("rename artist: write failed", "song", s.ID, "error", err)
					failedTracks = append(failedTracks, s.ID)
					continue
				}
				if isExactSolo {
					renamedTracks = append(renamedTracks, s.ID)
				} else {
					preservedFeatures = append(preservedFeatures, s.ID)
				}
			}

			if err := nd.StartScan(ctx); err != nil {
				slog.Error("rename artist: scan trigger failed", "artist", artistID, "error", err)
			}

			// Restore the captured photo against the new artist id
			// once the rescan has produced it. FindArtistIDByName uses
			// search3 internally; the rescan should have indexed the
			// new name before publish.
			var newArtistID string
			if len(savedPhotoBytes) > 0 {
				id, err := nd.FindArtistIDByName(ctx, newName)
				if err != nil {
					slog.Warn("rename artist: lookup new id for photo carry-over failed", "newName", newName, "error", err)
				} else if id == "" {
					slog.Warn("rename artist: new artist not found after scan, photo not carried over", "newName", newName)
				} else if err := nd.UploadArtistImage(ctx, id, savedPhotoBytes, "artist.jpg"); err != nil {
					slog.Error("rename artist: photo carry-over upload failed", "newID", id, "error", err)
				} else {
					slog.Info("rename artist: photo carried over", "oldID", artistID, "newID", id, "bytes", len(savedPhotoBytes))
					newArtistID = id
				}
			}

			slog.Info("rename artist: complete",
				"artist", artistID,
				"renamed", len(renamedTracks),
				"preserved_features", len(preservedFeatures),
				"failed", len(failedTracks),
			)

			if hub != nil {
				hub.Publish(Event{
					Type: "library_updated",
					Data: map[string]any{
						// Marker that this event represents a rename
						// completion, distinct from generic library
						// touches. Frontend matches on these fields to
						// surface the cascade summary toast.
						"operation":               "artist_rename",
						"complete":                true,
						"renamed_artist":          artistID,
						"new_artist_id":           newArtistID,
						"old_name":                oldName,
						"new_name":                newName,
						"renamed_track_count":     len(renamedTracks),
						"feature_preserved_count": len(preservedFeatures),
						"failed_count":            len(failedTracks),
					},
				})
			}
		}()

		// 202 Accepted: work has been queued, watch SSE for completion.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted":     true,
			"artist_id":    artistID,
			"old_name":     oldName,
			"new_name":     newName,
			"songs_queued": len(songs),
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

		// Bump audio file mtimes so Navidrome's incremental scan
		// re-reads the album and re-applies CoverArtPriority. Without
		// this the scan would notice nothing changed (cover files
		// alone don't trigger an album re-evaluation) and the cached
		// embedded-art coverArt id would stick. The audio bytes are
		// untouched — only the file's modification timestamp moves.
		touched := time.Now()
		for _, s := range songs.Songs {
			if s.Path == "" {
				continue
			}
			audioPath := nd.HostPath(s.Path)
			if err := os.Chtimes(audioPath, touched, touched); err != nil {
				slog.Warn("upload album cover: touch audio file failed", "path", audioPath, "error", err)
			}
		}

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

// handleUploadArtistPhoto installs a custom artist photo via
// Navidrome's native upload API. Unlike album cover art, Navidrome
// does NOT consult folder-level artist.* files reliably — its
// ArtistArtPriority is evaluated alongside external agents (LastFM,
// MusicBrainz) and the external lookup often wins for known artists.
// The native /api/artist/{id}/image endpoint sidesteps that priority
// chain entirely: Navidrome stores the upload in its uploaded_image
// column and serves it from getCoverArt immediately, no rescan
// required.
//
// Wire shape is a raw PUT body with image/jpeg or image/png
// Content-Type, matching the album-cover endpoint for consistency.
// We repackage as multipart server-side before forwarding.
func handleUploadArtistPhoto(_ *config.Config, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		artistID := r.PathValue("id")
		if artistID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_id", "Artist ID is required.")
			return
		}

		ext, err := coverExtFromContentType(r.Header.Get("Content-Type"))
		if err != nil {
			writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_format", err.Error())
			return
		}

		// Buffer the upload in memory. Artist photos are bounded to
		// maxCoverBytes (10 MiB) so the in-memory cost is acceptable
		// and we get retry semantics for free (doNative may re-build
		// the multipart on a 401 → re-auth).
		body, err := io.ReadAll(io.LimitReader(http.MaxBytesReader(w, r.Body, maxCoverBytes), maxCoverBytes+1))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "write_failed", "Failed to read upload: "+err.Error())
			return
		}
		if len(body) == 0 {
			writeJSONError(w, http.StatusBadRequest, "empty_body", "Request body was empty.")
			return
		}
		if int64(len(body)) > maxCoverBytes {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "too_large", "Artist photo exceeds size limit.")
			return
		}

		filename := "artist" + ext
		if err := nd.UploadArtistImage(r.Context(), artistID, body, filename); err != nil {
			slog.Error("upload artist photo: navidrome upload failed", "artist", artistID, "error", err)
			writeJSONError(w, http.StatusBadGateway, "upload_failed", "Failed to upload via Navidrome: "+err.Error())
			return
		}

		slog.Info("upload artist photo: installed via native api", "artist", artistID, "bytes", len(body), "user", userID)

		if hub != nil {
			hub.Publish(Event{
				Type: "library_updated",
				Data: map[string]any{"updated_artist_photo": artistID},
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"updated":   true,
			"artist_id": artistID,
			"bytes":     len(body),
		})
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
