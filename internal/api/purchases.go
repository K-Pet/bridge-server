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

// All Supabase calls in this file run with the user's forwarded JWT
// (via bearerToken from onboarding.go) plus the project anon key.
// PostgREST queries hit RLS, which on these tables permits:
//
//   - purchases / purchase_items: SELECT WHERE user_id = auth.uid()
//   - tracks / albums:            public SELECT (catalog rows)
//   - purchase_tracks (view):     security_invoker, follows the above
//
// Storage URL signing for the `tracks` private bucket can't go through
// RLS — it's a bucket-level permission that requires service-role.
// That work runs through marketplace's get-download-urls Edge Function
// (extended in Phase 2b to accept track_ids[]) which signs server-side
// using its own service role and returns short-lived URLs.

func handlePurchases(cfg *config.Config, queue *store.Queue) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" {
			json.NewEncoder(w).Encode([]any{})
			return
		}

		// Embed album + track titles so the Purchases page can render
		// item names without a second round-trip. RLS gates rows to
		// the caller's own purchases (user_id = auth.uid()), so a
		// no-filter SELECT here is safe — equivalent to the explicit
		// user_id=eq filter the previous service-role version had.
		queryURL := cfg.SupabaseURL + "/rest/v1/purchases?" +
			"select=id,total_cents,status,payment_ref,created_at," +
			"purchase_items(id,track_id,album_id,price_cents," +
			"track:tracks(id,title,artist,album_id,format,disc_number,album_index)," +
			"album:albums(id,title,artist,cover_art_url," +
			"tracks(id,title,artist,format,disc_number,album_index)))" +
			"&order=created_at.desc&limit=200"

		req, err := http.NewRequestWithContext(r.Context(), "GET", queryURL, nil)
		if err != nil {
			slog.Error("failed to build purchase query", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("apikey", cfg.SupabaseAnonKey)
		req.Header.Set("Authorization", "Bearer "+jwt)

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
						row["delivery"] = map[string]any{"total": 0}
					}
				}
			}
		}

		json.NewEncoder(w).Encode(rows)
	}
}

// handleRedeliver resets the local task queue for a purchase and re-
// invokes the marketplace's retry-purchase-delivery Edge Function so
// tracks are re-downloaded. retry-purchase-delivery is the user-JWT
// twin of deliver-purchase — it verifies ownership and fans out.
func handleRedeliver(cfg *config.Config, queue *store.Queue) http.HandlerFunc {
	client := &http.Client{Timeout: 30 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		if userID == "" || jwt == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		purchaseID := r.PathValue("id")
		if purchaseID == "" {
			http.Error(w, "missing purchase id", http.StatusBadRequest)
			return
		}

		// Ownership check via PostgREST + user JWT — RLS gates the
		// row to the caller's own purchase, so a 0-row response
		// means they don't own it.
		if err := verifyPurchaseOwner(r.Context(), client, cfg, jwt, purchaseID); err != nil {
			slog.Warn("redeliver: ownership check failed", "purchase", purchaseID, "user", userID, "error", err)
			http.Error(w, "purchase not found", http.StatusNotFound)
			return
		}

		// Clear local queue entries so retry-purchase-delivery can
		// re-enqueue fresh tasks.
		if queue != nil {
			if err := queue.DeleteTasksForPurchase(purchaseID); err != nil {
				slog.Warn("redeliver: failed to clear tasks", "purchase", purchaseID, "error", err)
			}
		}

		// retry-purchase-delivery flips status to delivering and
		// invokes deliver-purchase internally with service role. We
		// just forward the user JWT.
		deliveryErr := invokeRetryDelivery(r.Context(), client, cfg, jwt, purchaseID)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"purchase_id":    purchaseID,
			"status":         "delivering",
			"delivery_error": errString(deliveryErr),
		})
	}
}

// invokeRetryDelivery POSTs to retry-purchase-delivery with the
// user's JWT — replaces the old deliver-purchase + service-role path.
func invokeRetryDelivery(ctx context.Context, client *http.Client, cfg *config.Config, jwt, purchaseID string) error {
	body, _ := json.Marshal(map[string]string{"purchase_id": purchaseID})
	reqURL := cfg.SupabaseURL + "/functions/v1/retry-purchase-delivery"
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build retry request: %w", err)
	}
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("retry-purchase-delivery unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("retry-purchase-delivery %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// handleTrackDownload returns a fresh signed URL for a single track
// the user owns. Authorization happens server-side inside
// get-download-urls — we just forward the user JWT.
func handleTrackDownload(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		if userID == "" || jwt == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		trackID := r.PathValue("id")
		if trackID == "" {
			http.Error(w, "missing track id", http.StatusBadRequest)
			return
		}

		signed, err := getDownloadURLs(r.Context(), client, cfg, jwt, []string{trackID})
		if err != nil {
			slog.Error("download: get-download-urls failed", "track", trackID, "error", err)
			http.Error(w, "failed to generate download link", http.StatusInternalServerError)
			return
		}
		if len(signed) == 0 {
			writeJSONError(w, http.StatusForbidden, "not_owned", "You do not own this track.")
			return
		}
		first := signed[0]

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"url":      first.DownloadURL,
			"filename": buildFilename(first.Artist, first.Title, first.Format),
		})
	}
}

// signedTrack mirrors the wire shape of a single get-download-urls
// item — same fields the EF returns whether you ask by purchase_id,
// track_ids, or album_id-resolved-via-track_ids.
type signedTrack struct {
	TrackID     string `json:"track_id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Format      string `json:"format"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

// getDownloadURLs calls the marketplace's get-download-urls Edge
// Function with the user JWT and an explicit list of track ids. The
// EF verifies each id belongs to one of the user's purchases and
// returns signed URLs for the ones it owns (silently dropping the
// rest).
func getDownloadURLs(ctx context.Context, client *http.Client, cfg *config.Config, jwt string, trackIDs []string) ([]signedTrack, error) {
	body, _ := json.Marshal(map[string]any{"track_ids": trackIDs})
	reqURL := cfg.SupabaseURL + "/functions/v1/get-download-urls"
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get-download-urls unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("get-download-urls %d: %s", resp.StatusCode, string(respBody))
	}

	var out struct {
		Tracks []signedTrack `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Tracks, nil
}

// handleAlbumZip streams a ZIP of every track in an album the caller
// owns. Files are fetched from Supabase Storage server-side via signed
// URLs and written directly to the response — nothing buffered to
// disk.
//
// Album → tracks resolution lives in two steps now: (1) public RLS
// SELECT against `tracks.album_id` lists the tracks in the album,
// (2) get-download-urls drops the ones the caller doesn't own.
func handleAlbumZip(cfg *config.Config) http.HandlerFunc {
	// Long timeout: a full FLAC album can be hundreds of MB.
	client := &http.Client{Timeout: 10 * time.Minute}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		if userID == "" || jwt == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		albumID := r.PathValue("id")
		if albumID == "" {
			http.Error(w, "missing album id", http.StatusBadRequest)
			return
		}

		album, tracks, err := fetchAlbumWithTracks(r.Context(), client, cfg, jwt, albumID)
		if err != nil {
			slog.Error("zip: album fetch failed", "album", albumID, "error", err)
			http.Error(w, "album not found", http.StatusNotFound)
			return
		}
		if len(tracks) == 0 {
			http.Error(w, "album has no tracks", http.StatusNotFound)
			return
		}

		// Sort by disc / index before signing so the zip preserves
		// album order without depending on EF response order.
		sortZipTracks(tracks)

		ids := make([]string, 0, len(tracks))
		for _, t := range tracks {
			ids = append(ids, t.ID)
		}
		signed, err := getDownloadURLs(r.Context(), client, cfg, jwt, ids)
		if err != nil {
			slog.Error("zip: get-download-urls failed", "album", albumID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(signed) == 0 {
			writeJSONError(w, http.StatusForbidden, "not_owned", "You do not own this album.")
			return
		}

		// Build a track_id → signed-track map so we can keep our
		// pre-sorted order while pulling URLs from the EF response.
		signedByID := make(map[string]signedTrack, len(signed))
		for _, s := range signed {
			signedByID[s.TrackID] = s
		}

		zipName := buildFilename(album.Artist, album.Title, "zip")
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, zipName))

		zw := zip.NewWriter(w)
		defer zw.Close()

		for _, t := range tracks {
			s, owned := signedByID[t.ID]
			if !owned || s.DownloadURL == "" {
				// Track not owned (or sign failed) — skip rather
				// than 403 the whole album response. The user gets
				// a partial zip of the parts they bought.
				slog.Warn("zip: skipping unowned/unsigned track", "track", t.ID, "album", albumID)
				continue
			}
			if err := streamTrackIntoZip(r.Context(), client, zw, t, s); err != nil {
				slog.Error("zip: failed to add track", "track", t.ID, "error", err)
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
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Format     string `json:"format"`
	DiscNumber *int   `json:"disc_number"`
	AlbumIndex *int   `json:"album_index"`
}

// fetchAlbumWithTracks reads the album row + its tracks via the user
// JWT. albums + tracks have public RLS so anon-key + JWT is fine —
// no service role needed.
func fetchAlbumWithTracks(ctx context.Context, client *http.Client, cfg *config.Config, jwt, albumID string) (*albumMeta, []zipTrack, error) {
	queryURL := fmt.Sprintf(
		"%s/rest/v1/albums?id=eq.%s&select=id,title,artist,tracks(id,title,artist,format,disc_number,album_index)",
		cfg.SupabaseURL, albumID,
	)
	req, _ := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)

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
	return &rows[0].albumMeta, rows[0].Tracks, nil
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

func streamTrackIntoZip(ctx context.Context, client *http.Client, zw *zip.Writer, t zipTrack, s signedTrack) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.DownloadURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("fetch status %d: %s", resp.StatusCode, string(body))
	}

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

// verifyPurchaseOwner checks via PostgREST + user JWT that the
// caller owns the purchase. RLS on `purchases` only returns the row
// if user_id = auth.uid(), so a 0-row response means the caller
// either doesn't own it or it doesn't exist (we treat both as 404).
func verifyPurchaseOwner(ctx context.Context, client *http.Client, cfg *config.Config, jwt, purchaseID string) error {
	queryURL := fmt.Sprintf("%s/rest/v1/purchases?id=eq.%s&select=id", cfg.SupabaseURL, purchaseID)
	req, _ := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("purchase not found")
	}
	return nil
}

// escapePathSegments URL-encodes each "/"-separated segment of a
// storage path while preserving the slashes themselves. Kept here in
// case future code re-introduces direct storage signing — the
// Phase 2b refactor moves all signing into get-download-urls so
// nothing in this file calls it today.
func escapePathSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// _ keeps escapePathSegments compiled if other files start using it.
var _ = escapePathSegments

func buildFilename(artist, title, format string) string {
	name := title
	if artist != "" {
		name = artist + " - " + title
	}
	for _, bad := range []string{"/", "\\", "\"", "\x00"} {
		name = strings.ReplaceAll(name, bad, "_")
	}
	if format != "" {
		name += "." + format
	}
	return name
}

// handleEntitlements returns the album_ids and track_ids the user has
// purchased. Tracks belonging to a purchased album are expanded so
// the marketplace UI can show "Owned" on both album and track without
// extra client-side joins.
func handleEntitlements(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		jwt := bearerToken(r)
		w.Header().Set("Content-Type", "application/json")

		empty := map[string]any{"album_ids": []string{}, "track_ids": []string{}}
		if userID == "" || jwt == "" || cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" {
			json.NewEncoder(w).Encode(empty)
			return
		}

		albumIDs, trackIDs, err := fetchEntitlements(r.Context(), client, cfg, jwt)
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

// fetchEntitlements returns deduped album_ids + track_ids the user
// owns. Reads from purchase_items (RLS joined to purchases by
// user_id), then expands album entitlements to per-track ids via the
// public-RLS tracks table.
func fetchEntitlements(ctx context.Context, client *http.Client, cfg *config.Config, jwt string) ([]string, []string, error) {
	// Embed the parent purchase via PostgREST's `!inner` foreign-key
	// hint so the RLS filter on purchases applies — purchase_items
	// itself doesn't carry user_id.
	itemsURL := cfg.SupabaseURL +
		"/rest/v1/purchase_items?select=track_id,album_id," +
		"purchase:purchases!inner(user_id,status)&limit=5000"
	req, err := http.NewRequestWithContext(ctx, "GET", itemsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build entitlements request: %w", err)
	}
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)

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

	if len(albumSet) > 0 {
		ids := make([]string, 0, len(albumSet))
		for id := range albumSet {
			ids = append(ids, id)
		}
		expandedTracks, err := fetchTrackIDsForAlbums(ctx, client, cfg, jwt, ids)
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

func fetchTrackIDsForAlbums(ctx context.Context, client *http.Client, cfg *config.Config, jwt string, albumIDs []string) ([]string, error) {
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
	req.Header.Set("apikey", cfg.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+jwt)

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
			"hcaptcha_site_key": cfg.HCaptchaSiteKey,
		}
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
