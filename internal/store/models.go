package store

import "time"

type TaskStatus string

const (
	StatusQueued      TaskStatus = "queued"
	StatusDownloading TaskStatus = "downloading"
	StatusWritten     TaskStatus = "written"
	StatusScanning    TaskStatus = "scanning"
	StatusComplete    TaskStatus = "complete"
	StatusFailed      TaskStatus = "failed"
)

type Purchase struct {
	ID          string  `json:"purchase_id"`
	UserID      string  `json:"user_id"`
	Tracks      []Track `json:"tracks"`
	Signature   string  `json:"signature"`
}

type Track struct {
	TrackID     string `json:"track_id"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Title       string `json:"title"`
	Format      string `json:"format"`
	DownloadURL string `json:"download_url"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type DownloadTask struct {
	ID          string     `json:"id"`
	PurchaseID  string     `json:"purchase_id"`
	Track       Track      `json:"track"`
	Status      TaskStatus `json:"status"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const MaxDownloadSize = 500 * 1024 * 1024 // 500MB
