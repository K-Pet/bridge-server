package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/store"
)

func handlePurchases(cfg *config.Config, queue *store.Queue) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if cfg.SupabaseURL == "" || cfg.SupabaseServiceKey == "" {
			json.NewEncoder(w).Encode([]any{})
			return
		}

		// Embed album + track titles so the Purchases page can render item names
		// without a second round-trip. PostgREST nests foreign key relationships
		// via `fk_column(select)` syntax.
		// Expand album purchases into their tracks (nested `tracks(...)` under
		// the `albums` relationship) so each purchased track gets its own row
		// in the UI and a corresponding per-track download button.
		queryURL := fmt.Sprintf(
			"%s/rest/v1/purchases?user_id=eq.%s"+
				"&select=id,total_cents,status,payment_ref,created_at,"+
				"purchase_items(id,track_id,album_id,price_cents,"+
				"track:tracks(id,title,artist,album_id,format,disc_number,album_index),"+
				"album:albums(id,title,artist,cover_art_url,"+
				"tracks(id,title,artist,format,disc_number,album_index)))"+
				"&order=created_at.desc&limit=200",
			cfg.SupabaseURL, userID,
		)

		req, err := http.NewRequestWithContext(r.Context(), "GET", queryURL, nil)
		if err != nil {
			slog.Error("failed to build purchase query", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("apikey", cfg.SupabaseServiceKey)
		req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

		resp, err := client.Do(req)
		if err != nil {
			slog.Error("failed to query purchases", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			slog.Error("supabase purchase query failed", "status", resp.StatusCode, "body", string(body))
			json.NewEncoder(w).Encode([]any{})
			return
		}

		// Parse into generic maps so we can inject per-purchase delivery info
		var rows []map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			slog.Error("failed to decode purchases", "error", err)
			json.NewEncoder(w).Encode([]any{})
			return
		}

		if queue != nil && len(rows) > 0 {
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				if id, ok := row["id"].(string); ok {
					ids = append(ids, id)
				}
			}
			summaries, err := queue.SummariesForPurchases(ids)
			if err != nil {
				slog.Warn("failed to fetch task summaries", "error", err)
			} else {
				for _, row := range rows {
					id, _ := row["id"].(string)
					if s, ok := summaries[id]; ok {
						row["delivery"] = s
					} else {
						// No local tasks: either delivered long ago, or the
						// server hasn't received a webhook for it yet.
						row["delivery"] = map[string]any{"total": 0}
					}
				}
			}
		}

		json.NewEncoder(w).Encode(rows)
	}
}

// handleRedeliver resets the local task queue for a purchase and re-invokes the
// Supabase deliver-purchase Edge Function so tracks are re-downloaded. Useful
// when a file has been deleted from disk or the initial delivery got stuck.
func handleRedeliver(cfg *config.Config, queue *store.Queue) http.HandlerFunc {
	client := &http.Client{Timeout: 30 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		purchaseID := r.PathValue("id")
		if purchaseID == "" {
			http.Error(w, "missing purchase id", http.StatusBadRequest)
			return
		}

		// Verify ownership: look up the purchase and confirm user_id matches
		if err := verifyPurchaseOwner(r.Context(), client, cfg, purchaseID, userID); err != nil {
			slog.Warn("redeliver: ownership check failed", "purchase", purchaseID, "user", userID, "error", err)
			http.Error(w, "purchase not found", http.StatusNotFound)
			return
		}

		// Clear local queue entries so deliver-purchase can re-enqueue fresh tasks
		if queue != nil {
			if err := queue.DeleteTasksForPurchase(purchaseID); err != nil {
				slog.Warn("redeliver: failed to clear tasks", "purchase", purchaseID, "error", err)
			}
		}

		// Reset purchase status to pending so the reconcile loop can update it again
		if err := patchPurchaseStatus(r.Context(), client, cfg, purchaseID, "pending"); err != nil {
			slog.Warn("redeliver: status reset failed", "purchase", purchaseID, "error", err)
		}

		// Fire the Edge Function — same entry point used by the initial purchase flow
		deliveryErr := triggerDelivery(r.Context(), client, cfg, purchaseID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"purchase_id":    purchaseID,
			"status":         "pending",
			"delivery_error": errString(deliveryErr),
		})
	}
}

// handleTrackDownload returns a fresh Supabase Storage signed URL for a track
// the user owns. The browser opens the URL directly (no proxy), so the user
// gets a native browser download at full speed.
func handleTrackDownload(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		trackID := r.PathValue("id")
		if trackID == "" {
			http.Error(w, "missing track id", http.StatusBadRequest)
			return
		}

		// Ownership: either the track or its parent album must be in entitlements
		_, ownedTracks, err := fetchEntitlements(r.Context(), client, cfg, userID)
		if err != nil {
			slog.Error("download: entitlement check failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !contains(ownedTracks, trackID) {
			writeJSONError(w, http.StatusForbidden, "not_owned", "You do not own this track.")
			return
		}

		// Look up the storage path + a human-friendly filename
		meta, err := fetchTrackStorage(r.Context(), client, cfg, trackID)
		if err != nil {
			slog.Error("download: track lookup failed", "track", trackID, "error", err)
			http.Error(w, "track not found", http.StatusNotFound)
			return
		}
		if meta.StoragePath == "" {
			http.Error(w, "track has no storage path", http.StatusNotFound)
			return
		}

		filename := buildFilename(meta.Artist, meta.Title, meta.Format)

		// Pass the desired filename so Supabase Storage attaches a
		// Content-Disposition: attachment header — this makes the browser
		// download the file directly instead of opening an inline player.
		signedURL, err := createSignedURL(r.Context(), client, cfg, meta.StoragePath, filename)
		if err != nil {
			slog.Error("download: signed URL failed", "track", trackID, "error", err)
			http.Error(w, "failed to generate download link", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"url":      signedURL,
			"filename": filename,
		})
	}
}

type trackMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	StoragePath string `json:"storage_path"`
	Format      string `json:"format"`
}

func fetchTrackStorage(ctx context.Context, client *http.Client, cfg *config.Config, trackID string) (*trackMeta, error) {
	queryURL := fmt.Sprintf("%s/rest/v1/tracks?id=eq.%s&select=id,title,artist,storage_path,format", cfg.SupabaseURL, trackID)
	req, _ := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("track fetch failed: %d %s", resp.StatusCode, string(body))
	}
	var rows []trackMeta
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("track not found")
	}
	return &rows[0], nil
}

// createSignedURL asks Supabase Storage to mint a signed URL for the given path.
// Uses the REST API directly — equivalent to supabase.storage.from(bucket).createSignedUrl.
// If downloadFilename is non-empty, the returned URL gets a `?download=<name>`
// query parameter so Supabase Storage responds with a Content-Disposition
// attachment header, forcing the browser to download instead of inlining.
func createSignedURL(ctx context.Context, client *http.Client, cfg *config.Config, storagePath, downloadFilename string) (string, error) {
	body := []byte(`{"expiresIn":3600}`)
	// Encode each path segment separately so slashes stay as slashes. Using
	// url.PathEscape on the full path would turn "/" into "%2F", which makes
	// Supabase sign the escaped path while the browser later requests the
	// unescaped one, causing InvalidSignature failures.
	endpoint := fmt.Sprintf("%s/storage/v1/object/sign/tracks/%s", cfg.SupabaseURL, escapePathSegments(storagePath))
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("sign failed: %d %s", resp.StatusCode, string(respBody))
	}

	var signed struct {
		SignedURL string `json:"signedURL"`
		URL       string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		return "", err
	}
	path := signed.SignedURL
	if path == "" {
		path = signed.URL
	}
	if path == "" {
		return "", fmt.Errorf("empty signed URL")
	}
	// Normalize: the Storage API returns a path like "/object/sign/..."; prepend the base URL + /storage/v1
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	full := cfg.SupabaseURL + "/storage/v1" + path
	if downloadFilename != "" {
		sep := "?"
		if strings.Contains(full, "?") {
			sep = "&"
		}
		full += sep + "download=" + url.QueryEscape(downloadFilename)
	}
	return full, nil
}

// handleAlbumZip streams a ZIP of every track in an album the caller owns.
// Files are fetched from Supabase Storage server-side and written directly to
// the response — nothing is buffered to disk.
func handleAlbumZip(cfg *config.Config) http.HandlerFunc {
	// Long timeout: a full album of FLACs can be hundreds of MB.
	client := &http.Client{Timeout: 10 * time.Minute}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		albumID := r.PathValue("id")
		if albumID == "" {
			http.Error(w, "missing album id", http.StatusBadRequest)
			return
		}

		// Ownership: either album-level entitlement, OR every track in the
		// album is individually owned. Album-level is the common case.
		ownedAlbums, ownedTracks, err := fetchEntitlements(r.Context(), client, cfg, userID)
		if err != nil {
			slog.Error("zip: entitlement check failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		album, tracks, err := fetchAlbumWithTracks(r.Context(), client, cfg, albumID)
		if err != nil {
			slog.Error("zip: album fetch failed", "album", albumID, "error", err)
			http.Error(w, "album not found", http.StatusNotFound)
			return
		}
		if len(tracks) == 0 {
			http.Error(w, "album has no tracks", http.StatusNotFound)
			return
		}

		ownsAlbum := contains(ownedAlbums, albumID)
		if !ownsAlbum {
			for _, t := range tracks {
				if !contains(ownedTracks, t.ID) {
					writeJSONError(w, http.StatusForbidden, "not_owned", "You do not own this album.")
					return
				}
			}
		}

		zipName := buildFilename(album.Artist, album.Title, "zip")
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, zipName))

		zw := zip.NewWriter(w)
		defer zw.Close()

		for _, t := range tracks {
			if t.StoragePath == "" {
				slog.Warn("zip: track missing storage path, skipping", "track", t.ID)
				continue
			}
			if err := streamTrackIntoZip(r.Context(), client, cfg, zw, t); err != nil {
				slog.Error("zip: failed to add track", "track", t.ID, "error", err)
				// Stop here — we've already started writing the zip, can't
				// switch to an HTTP error. The partial zip is still valid.
				return
			}
		}
	}
}

type albumMeta struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type zipTrack struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	StoragePath string `json:"storage_path"`
	Format      string `json:"format"`
	DiscNumber  *int   `json:"disc_number"`
	AlbumIndex  *int   `json:"album_index"`
}

func fetchAlbumWithTracks(ctx context.Context, client *http.Client, cfg *config.Config, albumID string) (*albumMeta, []zipTrack, error) {
	queryURL := fmt.Sprintf(
		"%s/rest/v1/albums?id=eq.%s&select=id,title,artist,tracks(id,title,artist,storage_path,format,disc_number,album_index)",
		cfg.SupabaseURL, albumID,
	)
	req, _ := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("album fetch failed: %d %s", resp.StatusCode, string(body))
	}

	var rows []struct {
		albumMeta
		Tracks []zipTrack `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("album not found")
	}
	tracks := rows[0].Tracks
	// Stable ordering: disc, then album index. The library's order matters so
	// the zip listing matches the album track listing.
	sortZipTracks(tracks)
	return &rows[0].albumMeta, tracks, nil
}

func sortZipTracks(tracks []zipTrack) {
	intOr := func(p *int, fallback int) int {
		if p == nil {
			return fallback
		}
		return *p
	}
	for i := 1; i < len(tracks); i++ {
		j := i
		for j > 0 {
			a, b := tracks[j-1], tracks[j]
			da, db := intOr(a.DiscNumber, 1), intOr(b.DiscNumber, 1)
			if da < db || (da == db && intOr(a.AlbumIndex, 0) <= intOr(b.AlbumIndex, 0)) {
				break
			}
			tracks[j-1], tracks[j] = b, a
			j--
		}
	}
}

func streamTrackIntoZip(ctx context.Context, client *http.Client, cfg *config.Config, zw *zip.Writer, t zipTrack) error {
	signedURL, err := createSignedURL(ctx, client, cfg, t.StoragePath, "")
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", signedURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("fetch status %d: %s", resp.StatusCode, string(body))
	}

	// Prefix with track number so the zip preserves album order on disk.
	prefix := ""
	if t.AlbumIndex != nil {
		prefix = fmt.Sprintf("%02d - ", *t.AlbumIndex)
	}
	entryName := prefix + buildFilename(t.Artist, t.Title, t.Format)

	f, err := zw.Create(entryName)
	if err != nil {
		return fmt.Errorf("zip create: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("zip copy: %w", err)
	}
	return nil
}

func verifyPurchaseOwner(ctx context.Context, client *http.Client, cfg *config.Config, purchaseID, userID string) error {
	queryURL := fmt.Sprintf("%s/rest/v1/purchases?id=eq.%s&select=user_id", cfg.SupabaseURL, purchaseID)
	req, _ := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var rows []struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("purchase not found")
	}
	if rows[0].UserID != userID {
		return fmt.Errorf("purchase owned by another user")
	}
	return nil
}

func patchPurchaseStatus(ctx context.Context, client *http.Client, cfg *config.Config, purchaseID, status string) error {
	body := []byte(fmt.Sprintf(`{"status":%q}`, status))
	endpoint := fmt.Sprintf("%s/rest/v1/purchases?id=eq.%s", cfg.SupabaseURL, purchaseID)
	req, _ := http.NewRequestWithContext(ctx, "PATCH", endpoint, bytes.NewReader(body))
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// escapePathSegments URL-encodes each "/"-separated segment of a storage path
// while preserving the slashes themselves. Supabase Storage signs the literal
// path it receives, so escaping the full path (which would turn "/" into
// "%2F") causes InvalidSignature when the browser later fetches the URL with
// unescaped slashes.
func escapePathSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func buildFilename(artist, title, format string) string {
	name := title
	if artist != "" {
		name = artist + " - " + title
	}
	// Strip filesystem-unsafe characters so Content-Disposition parses cleanly
	for _, bad := range []string{"/", "\\", "\"", "\x00"} {
		name = strings.ReplaceAll(name, bad, "_")
	}
	if format != "" {
		name += "." + format
	}
	return name
}

// handleEntitlements returns the set of album_ids and track_ids the user has
// purchased. Tracks belonging to a purchased album are expanded and included
// in track_ids so the marketplace can show "Owned" on both the album and its
// individual tracks without extra client-side joins.
func handleEntitlements(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		w.Header().Set("Content-Type", "application/json")

		empty := map[string]any{"album_ids": []string{}, "track_ids": []string{}}
		if userID == "" || cfg.SupabaseURL == "" || cfg.SupabaseServiceKey == "" {
			json.NewEncoder(w).Encode(empty)
			return
		}

		albumIDs, trackIDs, err := fetchEntitlements(r.Context(), client, cfg, userID)
		if err != nil {
			slog.Error("failed to fetch entitlements", "error", err)
			json.NewEncoder(w).Encode(empty)
			return
		}

		json.NewEncoder(w).Encode(map[string]any{
			"album_ids": albumIDs,
			"track_ids": trackIDs,
		})
	}
}

// fetchEntitlements returns deduped album_ids + track_ids the user owns.
// Tracks belonging to a purchased album are included in track_ids.
func fetchEntitlements(ctx context.Context, client *http.Client, cfg *config.Config, userID string) ([]string, []string, error) {
	// Purchase items (direct ownership)
	itemsURL := fmt.Sprintf(
		"%s/rest/v1/purchase_items?select=track_id,album_id,purchase:purchases!inner(user_id,status)&purchase.user_id=eq.%s&limit=5000",
		cfg.SupabaseURL, userID,
	)
	req, err := http.NewRequestWithContext(ctx, "GET", itemsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build entitlements request: %w", err)
	}
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("entitlements query failed: %d %s", resp.StatusCode, string(body))
	}

	var items []struct {
		TrackID *string `json:"track_id"`
		AlbumID *string `json:"album_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, nil, err
	}

	albumSet := map[string]struct{}{}
	trackSet := map[string]struct{}{}
	for _, it := range items {
		if it.AlbumID != nil && *it.AlbumID != "" {
			albumSet[*it.AlbumID] = struct{}{}
		}
		if it.TrackID != nil && *it.TrackID != "" {
			trackSet[*it.TrackID] = struct{}{}
		}
	}

	// Expand album purchases to their tracks so per-track "Owned" works
	if len(albumSet) > 0 {
		ids := make([]string, 0, len(albumSet))
		for id := range albumSet {
			ids = append(ids, id)
		}
		expandedTracks, err := fetchTrackIDsForAlbums(ctx, client, cfg, ids)
		if err != nil {
			slog.Warn("failed to expand album tracks for entitlements", "error", err)
		} else {
			for _, id := range expandedTracks {
				trackSet[id] = struct{}{}
			}
		}
	}

	albumIDs := make([]string, 0, len(albumSet))
	for id := range albumSet {
		albumIDs = append(albumIDs, id)
	}
	trackIDs := make([]string, 0, len(trackSet))
	for id := range trackSet {
		trackIDs = append(trackIDs, id)
	}
	return albumIDs, trackIDs, nil
}

func fetchTrackIDsForAlbums(ctx context.Context, client *http.Client, cfg *config.Config, albumIDs []string) ([]string, error) {
	if len(albumIDs) == 0 {
		return nil, nil
	}
	inList := "("
	for i, id := range albumIDs {
		if i > 0 {
			inList += ","
		}
		inList += url.QueryEscape(id)
	}
	inList += ")"

	reqURL := fmt.Sprintf("%s/rest/v1/tracks?album_id=in.%s&select=id&limit=5000", cfg.SupabaseURL, inList)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build track expansion request: %w", err)
	}
	req.Header.Set("apikey", cfg.SupabaseServiceKey)
	req.Header.Set("Authorization", "Bearer "+cfg.SupabaseServiceKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("track expansion failed: %d %s", resp.StatusCode, string(body))
	}

	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

func handleConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"supabase_url":      cfg.SupabaseURL,
			"supabase_anon_key": cfg.SupabaseAnonKey,
			"dev_mode":          cfg.DevMode,
			"marketplace_url":   cfg.MarketplaceURL,
		}
		// Expose test credentials in dev mode so the frontend can auto-
		// sign-in and forward a real Supabase session to the marketplace
		// iframe. Never served in production.
		if cfg.DevMode && cfg.DevEmail != "" {
			payload["dev_email"] = cfg.DevEmail
			payload["dev_password"] = cfg.DevPassword
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload)
	}
}

func handleGetSettings(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"delivery_mode": cfg.DeliveryMode,
			"poll_interval": cfg.PollInterval.String(),
		})
	}
}
