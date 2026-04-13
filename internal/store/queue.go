package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Queue struct {
	db *sql.DB
}

func NewQueue(dataDir string) (*Queue, error) {
	dbPath := filepath.Join(dataDir, "bridge.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate queue db: %w", err)
	}
	return &Queue{db: db}, nil
}

func (q *Queue) Close() error {
	return q.db.Close()
}

func (q *Queue) Enqueue(purchaseID string, track Track) error {
	trackJSON, err := json.Marshal(track)
	if err != nil {
		return err
	}
	_, err = q.db.Exec(`
		INSERT INTO tasks (id, purchase_id, track, status, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)
	`, fmt.Sprintf("%s:%s", purchaseID, track.TrackID), purchaseID, trackJSON, StatusQueued, time.Now(), time.Now())
	return err
}

func (q *Queue) Next() (*DownloadTask, error) {
	row := q.db.QueryRow(`
		SELECT id, purchase_id, track, status, attempts, last_error, created_at, updated_at
		FROM tasks WHERE status = ? ORDER BY created_at ASC LIMIT 1
	`, StatusQueued)

	var task DownloadTask
	var trackJSON []byte
	err := row.Scan(&task.ID, &task.PurchaseID, &trackJSON, &task.Status, &task.Attempts, &task.LastError, &task.CreatedAt, &task.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(trackJSON, &task.Track); err != nil {
		return nil, err
	}
	return &task, nil
}

func (q *Queue) UpdateStatus(id string, status TaskStatus, lastError string) error {
	_, err := q.db.Exec(`
		UPDATE tasks SET status = ?, last_error = ?, attempts = attempts + 1, updated_at = ?
		WHERE id = ?
	`, status, lastError, time.Now(), id)
	return err
}

func (q *Queue) PendingCount() (int, error) {
	var count int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status IN (?, ?)`, StatusQueued, StatusDownloading).Scan(&count)
	return count, err
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			purchase_id TEXT NOT NULL,
			track TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued',
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
	`)
	return err
}
