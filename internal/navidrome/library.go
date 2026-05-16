package navidrome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SongInfo holds song metadata from the Navidrome native API.
type SongInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`      // track-level credit (can include "feat.")
	AlbumArtist string `json:"albumArtist"` // album-level grouping (stable across the album)
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

	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", url, nil)
	})
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

// GetArtistSongs retrieves every song attributed to the given artist
// id. Used by the artist-level metadata edit flow so a single rename
// can propagate across the artist's whole discography in one pass.
//
// The native API has no offset semantics for filtered song lists, but
// the _end=10000 cap is comfortably larger than any plausible
// artist's catalog (the largest classical-composer pages on
// MusicBrainz are ~6k recordings). If we ever hit it, paginate.
func (c *Client) GetArtistSongs(ctx context.Context, artistID string) ([]SongInfo, error) {
	url := fmt.Sprintf("%s/api/song?_end=10000&_order=ASC&_sort=album,discNumber,trackNumber&_start=0&artist_id=%s",
		c.baseURL, artistID)

	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", url, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("get artist songs request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get artist songs returned status %d: %s", resp.StatusCode, string(body))
	}

	var songs []SongInfo
	if err := json.NewDecoder(resp.Body).Decode(&songs); err != nil {
		return nil, fmt.Errorf("failed to decode artist songs response: %w", err)
	}
	return songs, nil
}

// GetAlbumSongs retrieves all songs in an album from Navidrome via the native API.
func (c *Client) GetAlbumSongs(ctx context.Context, albumID string) (*AlbumSongs, error) {
	// The native API supports filtering songs by albumId.
	url := fmt.Sprintf("%s/api/song?_end=500&_order=ASC&_sort=album,discNumber,trackNumber&_start=0&album_id=%s",
		c.baseURL, albumID)

	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", url, nil)
	})
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

// ArtistDetails is the slice of native /api/artist/{id} we care about
// for the rename carry-over flow. UploadedImage is the filename
// Navidrome stores in its uploaded_image column; non-empty means the
// artist has a custom photo we should preserve across a rename.
type ArtistDetails struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	UploadedImage string `json:"uploadedImage"`
}

// GetArtistDetails fetches an artist's native-API record. Returns
// ErrNotFound when the id no longer resolves (e.g. just renamed away).
func (c *Client) GetArtistDetails(ctx context.Context, artistID string) (*ArtistDetails, error) {
	url := fmt.Sprintf("%s/api/artist/%s", c.baseURL, artistID)
	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", url, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("get artist details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("artist not found")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("get artist details %d: %s", resp.StatusCode, string(body))
	}
	var details ArtistDetails
	if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
		return nil, fmt.Errorf("decode artist details: %w", err)
	}
	return &details, nil
}

// FetchCoverArt returns the raw bytes Navidrome serves at
// /rest/getCoverArt for the given id. We pass the full id including
// the "ar-{id}_0" prefix because Subsonic's contract uses that form.
// No size param → original bytes.
//
// Used by the rename flow to capture an artist's uploaded photo
// before the rename invalidates the artist row, then re-upload it
// against the new id once the rescan settles.
func (c *Client) FetchCoverArt(ctx context.Context, coverArtID string) ([]byte, error) {
	params := c.subsonicParams()
	params.Set("id", coverArtID)
	url := fmt.Sprintf("%s/rest/getCoverArt?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch coverart: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coverart returned %d", resp.StatusCode)
	}
	// Subsonic returns a JSON error envelope on auth failures via the
	// same status code; the bytes start with `{` rather than image
	// magic when that happens. Cheap sniff to avoid storing JSON
	// where we think we have a photo.
	const maxFetch = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetch+1))
	if err != nil {
		return nil, fmt.Errorf("read coverart: %w", err)
	}
	if int64(len(body)) > maxFetch {
		return nil, fmt.Errorf("coverart exceeds %d byte cap", maxFetch)
	}
	if len(body) > 0 && body[0] == '{' {
		return nil, fmt.Errorf("coverart endpoint returned JSON (likely an error)")
	}
	return body, nil
}

// FindArtistIDByName resolves an artist's current Navidrome id from
// their name via Subsonic search3. Match is case-insensitive on
// trimmed names. Returns "" when no exact match is found (the caller
// decides whether that's a hard error or a soft skip).
//
// Used by the rename flow: after the rescan we don't know the new
// hash-based id directly, so we look it up by the new name.
func (c *Client) FindArtistIDByName(ctx context.Context, name string) (string, error) {
	params := c.subsonicParams()
	params.Set("query", name)
	params.Set("artistCount", "10")
	params.Set("albumCount", "0")
	params.Set("songCount", "0")
	url := fmt.Sprintf("%s/rest/search3?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search3 request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search3 returned %d", resp.StatusCode)
	}
	var envelope struct {
		SubsonicResponse struct {
			SearchResult3 struct {
				Artist []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"artist"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", fmt.Errorf("decode search3: %w", err)
	}
	target := strings.ToLower(strings.TrimSpace(name))
	for _, a := range envelope.SubsonicResponse.SearchResult3.Artist {
		if strings.ToLower(strings.TrimSpace(a.Name)) == target {
			return a.ID, nil
		}
	}
	return "", nil
}

// UploadArtistImage installs a custom artist photo via Navidrome's
// native upload endpoint (POST /api/artist/{id}/image with a multipart
// `image` field). Navidrome 0.61+ stores it in the artist's
// `uploaded_image` column, takes ownership of caching/resizing, and
// serves it from /rest/getCoverArt — bypassing the ArtistArtPriority
// folder-vs-external lookup entirely.
//
// This is preferable to writing artist.jpg into the artist's folder
// because Navidrome's default behavior consults external services
// (MusicBrainz/LastFM) for artist images even when artist.* would
// match — the priority hint is read at scan time but the actual
// fetch can race against it. A direct upload sidesteps the priority
// path entirely.
func (c *Client) UploadArtistImage(ctx context.Context, artistID string, body []byte, filename string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", filename)
	if err != nil {
		return fmt.Errorf("create multipart form: %w", err)
	}
	if _, err := part.Write(body); err != nil {
		return fmt.Errorf("write multipart body: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}
	contentType := mw.FormDataContentType()
	payload := buf.Bytes()
	url := fmt.Sprintf("%s/api/artist/%s/image", c.baseURL, artistID)

	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("upload artist image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload artist image returned %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// PurgeMissing removes all missing-file references from Navidrome's
// database via DELETE /api/missing. This is the equivalent of going to
// Settings > Missing Files in the Navidrome UI and clicking delete all.
// Should be called after a scan has marked deleted files as missing.
func (c *Client) PurgeMissing(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/missing", c.baseURL)

	resp, err := c.doNative(ctx, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "DELETE", url, nil)
	})
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
