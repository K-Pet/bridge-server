package navidrome

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SongInfo holds song metadata from the Navidrome native API.
type SongInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumID     string `json:"albumId"`
	Path        string `json:"path"`        // relative path from libraryPath (real filesystem path)
	LibraryPath string `json:"libraryPath"` // music folder root (e.g. "/music")
}

// AlbumSongs holds an album's songs.
type AlbumSongs struct {
	AlbumID string
	Songs   []SongInfo
}

// GetSong retrieves song metadata (including real file path) from Navidrome
// via the native API. The Subsonic API returns a computed path from ID3 tags
// which doesn't match the actual filesystem; the native API returns the real
// relative path from the library root.
func (c *Client) GetSong(ctx context.Context, songID string) (*SongInfo, error) {
	url := fmt.Sprintf("%s/api/song/%s", c.baseURL, songID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+c.jwt)
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get song request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("song not found")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get song returned status %d: %s", resp.StatusCode, string(body))
	}

	var song SongInfo
	if err := json.NewDecoder(resp.Body).Decode(&song); err != nil {
		return nil, fmt.Errorf("failed to decode song response: %w", err)
	}
	if song.ID == "" {
		return nil, fmt.Errorf("song not found")
	}

	return &song, nil
}

// GetAlbumSongs retrieves all songs in an album from Navidrome via the native API.
func (c *Client) GetAlbumSongs(ctx context.Context, albumID string) (*AlbumSongs, error) {
	// The native API supports filtering songs by albumId.
	url := fmt.Sprintf("%s/api/song?_end=500&_order=ASC&_sort=album,discNumber,trackNumber&_start=0&album_id=%s",
		c.baseURL, albumID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+c.jwt)
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get album songs request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get album songs returned status %d: %s", resp.StatusCode, string(body))
	}

	var songs []SongInfo
	if err := json.NewDecoder(resp.Body).Decode(&songs); err != nil {
		return nil, fmt.Errorf("failed to decode album songs response: %w", err)
	}

	return &AlbumSongs{
		AlbumID: albumID,
		Songs:   songs,
	}, nil
}

// PurgeMissing removes all missing-file references from Navidrome's
// database via DELETE /api/missing. This is the equivalent of going to
// Settings > Missing Files in the Navidrome UI and clicking delete all.
// Should be called after a scan has marked deleted files as missing.
func (c *Client) PurgeMissing(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/missing", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-ND-Authorization", "Bearer "+c.jwt)
	req.Header.Set("X-ND-Client-Unique-Id", "bridge-server")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("purge missing files failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("purge missing returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DeleteSongFiles removes song files from the host filesystem. Navidrome
// container paths are translated to host paths via HostPath. Returns the
// number of files successfully removed.
func (c *Client) DeleteSongFiles(songs []SongInfo) (int, error) {
	if len(songs) == 0 {
		return 0, fmt.Errorf("no songs to delete")
	}

	var removed int
	for _, song := range songs {
		if song.Path == "" {
			slog.Warn("delete: song has no file path, skipping disk delete", "song", song.ID)
			continue
		}
		hostPath := c.HostPath(song.Path)
		if err := DeleteSongFile(hostPath); err != nil {
			slog.Warn("delete: file removal failed", "song", song.ID, "ndPath", song.Path, "hostPath", hostPath, "error", err)
		} else {
			removed++
		}
	}

	// Clean up cover art if the album directory only has cover files left.
	cleanedDirs := map[string]bool{}
	for _, song := range songs {
		if song.Path == "" {
			continue
		}
		albumDir := filepath.Dir(c.HostPath(song.Path))
		if cleanedDirs[albumDir] {
			continue
		}
		cleanedDirs[albumDir] = true
		removeCoverArtOnly(albumDir)
	}

	if removed == 0 {
		return 0, fmt.Errorf("no files were removed from disk")
	}
	return removed, nil
}

// ScanAndPurge triggers a Navidrome library scan (which marks deleted files
// as missing) and then purges all missing-file references. Intended to run
// in the background after files have already been removed from disk.
func (c *Client) ScanAndPurge(ctx context.Context) error {
	if err := c.StartScan(ctx); err != nil {
		return fmt.Errorf("scan after deletion failed: %w", err)
	}

	if err := c.PurgeMissing(ctx); err != nil {
		return fmt.Errorf("purge missing files failed: %w", err)
	}
	return nil
}

// DeleteSongFile removes a song's audio file from disk. If the parent
// directory (album folder) is empty after deletion, it is also removed,
// and the same cleanup is applied to the artist folder above it.
func DeleteSongFile(songPath string) error {
	if songPath == "" {
		return fmt.Errorf("empty song path")
	}
	if err := os.Remove(songPath); err != nil {
		return fmt.Errorf("remove file: %w", err)
	}

	// Clean up empty album directory
	albumDir := filepath.Dir(songPath)
	removeIfEmpty(albumDir)

	// Clean up empty artist directory
	artistDir := filepath.Dir(albumDir)
	removeIfEmpty(artistDir)

	return nil
}

// removeCoverArtOnly checks if a directory contains only cover art files
// (no audio). If so, it removes the cover files and the directory.
func removeCoverArtOnly(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			return // has subdirectories, leave it alone
		}
		name := strings.ToLower(e.Name())
		if !isCoverFile(name) {
			return // has a non-cover file (audio), leave it alone
		}
	}
	// Only cover files remain — remove them and the directory
	for _, e := range entries {
		os.Remove(filepath.Join(dir, e.Name()))
	}
	os.Remove(dir)
	// Also try to remove empty artist dir above
	removeIfEmpty(filepath.Dir(dir))
}

func isCoverFile(name string) bool {
	prefixes := []string{"cover.", "folder.", "front.", "album."}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// removeIfEmpty deletes a directory only if it contains no files or
// subdirectories. Errors are silently ignored — cleanup is best-effort.
func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		os.Remove(dir)
	}
}
