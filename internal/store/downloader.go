package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bridgemusic/bridge-server/internal/config"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
)

type Downloader struct {
	cfg      *config.Config
	queue    *Queue
	nd       *navidrome.Client
	client   *http.Client
}

func NewDownloader(cfg *config.Config, queue *Queue, nd *navidrome.Client) *Downloader {
	return &Downloader{
		cfg:    cfg,
		queue:  queue,
		nd:     nd,
		client: &http.Client{Timeout: 10 * time.Minute},
	}
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
			continue
		}

		slog.Info("download complete, triggering scan", "task", task.ID)
		d.queue.UpdateStatus(task.ID, StatusScanning, "")
		if err := d.nd.StartScan(ctx); err != nil {
			slog.Error("scan failed", "task", task.ID, "error", err)
		}
		d.queue.UpdateStatus(task.ID, StatusComplete, "")
	}
}

func (d *Downloader) process(ctx context.Context, task *DownloadTask) error {
	d.queue.UpdateStatus(task.ID, StatusDownloading, "")

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

	d.queue.UpdateStatus(task.ID, StatusWritten, "")
	return nil
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
