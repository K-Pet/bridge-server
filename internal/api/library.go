package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
	"github.com/bridgemusic/bridge-server/internal/store"
)

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
