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

// TasksForPurchase returns all tasks for the given purchase id.
func (q *Queue) TasksForPurchase(purchaseID string) ([]DownloadTask, error) {
	rows, err := q.db.Query(`
		SELECT id, purchase_id, track, status, attempts, last_error, created_at, updated_at
		FROM tasks WHERE purchase_id = ? ORDER BY created_at ASC
	`, purchaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DownloadTask
	for rows.Next() {
		var t DownloadTask
		var trackJSON []byte
		if err := rows.Scan(&t.ID, &t.PurchaseID, &trackJSON, &t.Status, &t.Attempts, &t.LastError, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(trackJSON, &t.Track); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTasksForPurchase removes all tasks for a purchase so they can be re-enqueued.
func (q *Queue) DeleteTasksForPurchase(purchaseID string) error {
	_, err := q.db.Exec(`DELETE FROM tasks WHERE purchase_id = ?`, purchaseID)
	return err
}

// PurchaseTaskSummary is a per-purchase aggregate of task statuses returned to clients.
type PurchaseTaskSummary struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"by_status"`
	Terminal   bool           `json:"terminal"`  // true if every task is in a terminal state (complete or failed)
	AllComplete bool          `json:"all_complete"` // true if every task is complete
	AnyFailed  bool           `json:"any_failed"`
	LastError  string         `json:"last_error,omitempty"`
}

// SummariesForPurchases returns a map of purchase_id → summary for the given ids.
// Purchases with zero tasks locally are omitted from the result.
func (q *Queue) SummariesForPurchases(purchaseIDs []string) (map[string]PurchaseTaskSummary, error) {
	out := map[string]PurchaseTaskSummary{}
	if len(purchaseIDs) == 0 {
		return out, nil
	}

	// Build placeholders for IN (?, ?, ...)
	placeholders := ""
	args := make([]any, len(purchaseIDs))
	for i, id := range purchaseIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT purchase_id, status, last_error
		FROM tasks WHERE purchase_id IN (%s)
	`, placeholders)

	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var pid, status, lastErr string
		if err := rows.Scan(&pid, &status, &lastErr); err != nil {
			return nil, err
		}
		s := out[pid]
		if s.ByStatus == nil {
			s.ByStatus = map[string]int{}
		}
		s.Total++
		s.ByStatus[status]++
		if status == string(StatusFailed) {
			s.AnyFailed = true
			if lastErr != "" {
				s.LastError = lastErr
			}
		}
		out[pid] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for pid, s := range out {
		terminal := true
		allComplete := s.Total > 0
		for status, count := range s.ByStatus {
			if status != string(StatusComplete) && status != string(StatusFailed) {
				terminal = false
				allComplete = false
			}
			if status != string(StatusComplete) && count > 0 {
				allComplete = false
			}
		}
		s.Terminal = terminal
		s.AllComplete = allComplete
		out[pid] = s
	}

	return out, nil
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
