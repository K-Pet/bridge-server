package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
)

// PurchaseStatusUpdater is implemented by a Supabase client.
// Kept as an interface here to avoid an import cycle with internal/supabase.
type PurchaseStatusUpdater interface {
	MarkPurchaseStatus(ctx context.Context, purchaseID, status string) error
}

// EventPublisher lets the downloader emit live progress events without
// importing internal/api (which would create a cycle). main.go passes in an
// adapter over the api.EventHub.
type EventPublisher interface {
	PublishTaskEvent(eventType, purchaseID, taskID, status string, data map[string]any)
}

type Downloader struct {
	cfg      *config.Config
	queue    *Queue
	nd       *navidrome.Client
	supabase PurchaseStatusUpdater
	events   EventPublisher
	client   *http.Client
}

func NewDownloader(cfg *config.Config, queue *Queue, nd *navidrome.Client, sb PurchaseStatusUpdater, ev EventPublisher) *Downloader {
	return &Downloader{
		cfg:      cfg,
		queue:    queue,
		nd:       nd,
		supabase: sb,
		events:   ev,
		client:   &http.Client{Timeout: 10 * time.Minute},
	}
}

func (d *Downloader) publish(eventType, purchaseID, taskID, status string, data map[string]any) {
	if d.events == nil {
		return
	}
	d.events.PublishTaskEvent(eventType, purchaseID, taskID, status, data)
}

// Run processes download tasks from the queue until the context is cancelled.
func (d *Downloader) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		task, err := d.queue.Next()
		if err != nil {
			slog.Error("failed to fetch next task", "error", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		if task == nil {
			sleep(ctx, 2*time.Second)
			continue
		}

		slog.Info("processing download", "task", task.ID, "track", task.Track.Title)
		if err := d.process(ctx, task); err != nil {
			slog.Error("download failed", "task", task.ID, "error", err)
			d.queue.UpdateStatus(task.ID, StatusFailed, err.Error())
			d.publish("task_status", task.PurchaseID, task.ID, string(StatusFailed), map[string]any{"error": err.Error()})
			d.reconcilePurchase(ctx, task.PurchaseID)
			continue
		}

		slog.Info("download complete, triggering scan", "task", task.ID)
		d.queue.UpdateStatus(task.ID, StatusScanning, "")
		d.publish("task_status", task.PurchaseID, task.ID, string(StatusScanning), nil)
		if err := d.nd.StartScan(ctx); err != nil {
			slog.Error("scan failed", "task", task.ID, "error", err)
		}
		d.queue.UpdateStatus(task.ID, StatusComplete, "")
		d.publish("task_status", task.PurchaseID, task.ID, string(StatusComplete), nil)
		d.publish("library_updated", task.PurchaseID, task.ID, "", nil)
		d.reconcilePurchase(ctx, task.PurchaseID)
	}
}

// reconcilePurchase checks if all tasks for a purchase are terminal and, if so,
// updates the Supabase purchase row to "delivered" (all complete) or "failed"
// (any task failed). Safe to call repeatedly — subsequent calls are idempotent.
func (d *Downloader) reconcilePurchase(ctx context.Context, purchaseID string) {
	if d.supabase == nil || purchaseID == "" {
		return
	}
	summaries, err := d.queue.SummariesForPurchases([]string{purchaseID})
	if err != nil {
		slog.Warn("reconcile: fetch summary failed", "purchase", purchaseID, "error", err)
		return
	}
	s, ok := summaries[purchaseID]
	if !ok || !s.Terminal {
		return
	}
	status := "delivered"
	if s.AnyFailed {
		status = "failed"
	}
	if err := d.supabase.MarkPurchaseStatus(ctx, purchaseID, status); err != nil {
		slog.Warn("reconcile: update supabase status failed", "purchase", purchaseID, "status", status, "error", err)
		return
	}
	slog.Info("reconcile: purchase marked", "purchase", purchaseID, "status", status)
}

func (d *Downloader) process(ctx context.Context, task *DownloadTask) error {
	d.queue.UpdateStatus(task.ID, StatusDownloading, "")
	d.publish("task_status", task.PurchaseID, task.ID, string(StatusDownloading), nil)

	track := task.Track
	if track.SizeBytes > MaxDownloadSize {
		return fmt.Errorf("track exceeds max size (%d > %d)", track.SizeBytes, MaxDownloadSize)
	}

	// Download to staging area
	incomingDir := filepath.Join(d.cfg.MusicDir, ".incoming")
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		return fmt.Errorf("create incoming dir: %w", err)
	}

	stagingPath := filepath.Join(incomingDir, task.ID+".part")
	defer os.Remove(stagingPath)

	req, err := http.NewRequestWithContext(ctx, "GET", track.DownloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	f, err := os.Create(stagingPath)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	written, err := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, MaxDownloadSize+1), hasher))
	f.Close()
	if err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	if written > MaxDownloadSize {
		return fmt.Errorf("download exceeded max size")
	}

	// Verify checksum
	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if track.SHA256 != "" && gotHash != track.SHA256 {
		return fmt.Errorf("checksum mismatch: got %s, want %s", gotHash, track.SHA256)
	}

	// Atomic move to final path
	finalDir := filepath.Join(d.cfg.MusicDir, "Bridge", sanitize(track.Artist), sanitize(track.Album))
	if err := os.MkdirAll(finalDir, 0755); err != nil {
		return fmt.Errorf("create album dir: %w", err)
	}
	finalPath := filepath.Join(finalDir, sanitize(track.Title)+"."+track.Format)

	if err := os.Rename(stagingPath, finalPath); err != nil {
		return fmt.Errorf("move to final path: %w", err)
	}

	// Drop the album cover next to the tracks so Navidrome indexes it.
	// Non-fatal: album art is a nice-to-have, and an audio file we
	// already wrote shouldn't get rolled back because a cover 404'd.
	if track.AlbumArtURL != "" {
		if err := d.ensureCoverArt(ctx, finalDir, track.AlbumArtURL); err != nil {
			slog.Warn("cover art download failed", "task", task.ID, "album", track.Album, "error", err)
		}
	}

	d.queue.UpdateStatus(task.ID, StatusWritten, "")
	d.publish("task_status", task.PurchaseID, task.ID, string(StatusWritten), map[string]any{"bytes": written})
	return nil
}

// ensureCoverArt writes the album cover to `<albumDir>/cover.<ext>` if
// one is not already present.  Called after every track write, but a
// quick stat on the sidecar skips the download when the file already
// exists — subsequent tracks in the same album are cheap no-ops.
//
// `coverURL` is the public URL publish-draft put on tracks.album_art_url
// (cache-busted with `?v=<ms>`).  We extract the extension from the
// path portion, falling back to `jpg`.
func (d *Downloader) ensureCoverArt(ctx context.Context, albumDir, coverURL string) error {
	ext := coverExtensionFromURL(coverURL)
	coverPath := filepath.Join(albumDir, "cover."+ext)

	// If any supported cover file already exists in this directory, bail.
	// Handles both the re-download-for-this-album case and the case where
	// Navidrome already picked up a different extension (jpeg → jpg).
	for _, candidate := range coverCandidates(albumDir) {
		if _, err := os.Stat(candidate); err == nil {
			return nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", coverURL, nil)
	if err != nil {
		return fmt.Errorf("cover request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("cover fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cover fetch status %d", resp.StatusCode)
	}

	// Stage and rename so we don't leave a half-written cover behind.
	staging := coverPath + ".part"
	f, err := os.Create(staging)
	if err != nil {
		return fmt.Errorf("create cover file: %w", err)
	}
	// Cap at 8 MiB — we shipped this ourselves as a compressed 1024×1024
	// JPEG, so anything larger is suspicious.
	const maxCoverBytes = 8 * 1024 * 1024
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxCoverBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(staging)
		return fmt.Errorf("write cover: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(staging)
		return fmt.Errorf("close cover: %w", closeErr)
	}
	if err := os.Rename(staging, coverPath); err != nil {
		os.Remove(staging)
		return fmt.Errorf("rename cover: %w", err)
	}
	slog.Info("cover art written", "path", coverPath)
	return nil
}

// coverExtensionFromURL pulls the extension off the path component,
// ignoring the query string.  Defaults to `jpg` for unrecognised inputs.
func coverExtensionFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "jpg"
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
	switch ext {
	case "jpg", "jpeg":
		return "jpg"
	case "png", "webp":
		return ext
	default:
		return "jpg"
	}
}

// coverCandidates enumerates the filenames Navidrome recognises as
// album cover art.  We skip the download when any of them exist to
// keep re-delivery / multi-track-album runs idempotent.
func coverCandidates(albumDir string) []string {
	names := []string{
		"cover.jpg", "cover.jpeg", "cover.png", "cover.webp",
		"folder.jpg", "folder.jpeg", "folder.png",
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(albumDir, n)
	}
	return out
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func sanitize(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, ".")
	if name == "" {
		name = "Unknown"
	}
	return name
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
