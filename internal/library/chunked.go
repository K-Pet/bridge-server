package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultChunkSize is what the server advertises to clients via
// BeginUpload. Picked well below Cloudflare's 100 MiB free-plan body
// cap and under typical reverse-proxy default body limits — every
// chunk is a fresh, short-lived request, dodging both the size cap
// and any long-stream timeout.
//
// Clients are free to send smaller chunks; the server doesn't care
// about the exact boundary. Larger chunks risk hitting the proxy
// limits this whole flow exists to avoid.
const DefaultChunkSize = 16 * 1024 * 1024 // 16 MiB

// pendingUpload tracks a single chunked-upload in flight. Each WriteChunk
// call appends to the staging file at the next expected offset; a
// Content-Range mismatch fails fast so the client knows to retry.
//
// A pending upload doesn't become a session Item until FinalizeUpload
// runs — that's when tags are read, the plan is computed, and the
// item joins the session for the review UI.
type pendingUpload struct {
	uploadID    string
	sessionID   string
	userID      string
	filename    string // basename, sanitized for path safety in stagingPath only
	size        int64  // total bytes the client said it would send
	stagingPath string // absolute path inside the session staging dir

	// mu serializes WriteChunk/FinalizeUpload so concurrent chunk PUTs
	// for the same upload don't collide on the file handle. The library
	// Manager mutex still protects the pendingUploads map itself.
	mu      sync.Mutex
	file    *os.File
	written int64
	updated time.Time
}

// pendingUploadTTL bounds how long a half-finished chunked upload
// can sit before the janitor reaps it. Aligned with SessionTTL so
// abandoned uploads die with their session.
const pendingUploadTTL = SessionTTL

// BeginUpload allocates a new chunked-upload slot for a session item.
// Caller streams bytes via WriteChunk and calls FinalizeUpload when
// every chunk has been ack'd. Returns the upload id the client uses
// to address subsequent chunks.
//
// Each upload mints its own item id up front so the staging filename
// is stable from chunk 0 — clients can reissue a chunk after a
// transient network error without the server forgetting the
// allocation.
func (m *Manager) BeginUpload(userID, sessionID, filename string, size int64) (uploadID string, err error) {
	if size <= 0 {
		return "", fmt.Errorf("size must be positive")
	}
	if size > MaxUploadBytes {
		return "", fmt.Errorf("file exceeds %d byte limit", MaxUploadBytes)
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if !IsSupportedAudio(ext) {
		return "", fmt.Errorf("unsupported file type %q", ext)
	}

	s, err := m.GetSession(userID, sessionID)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	if s.Status != SessionStatusOpen {
		m.mu.Unlock()
		return "", fmt.Errorf("session not accepting uploads (status=%s)", s.Status)
	}
	if len(s.Items)+len(m.pendingForSession(sessionID)) >= MaxSessionFiles {
		m.mu.Unlock()
		return "", fmt.Errorf("session full (max %d files)", MaxSessionFiles)
	}
	m.mu.Unlock()

	uploadID, err = randomID()
	if err != nil {
		return "", fmt.Errorf("upload id: %w", err)
	}

	stagingPath := filepath.Join(s.stagingDir, uploadID+ext)
	f, err := os.Create(stagingPath)
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}

	p := &pendingUpload{
		uploadID:    uploadID,
		sessionID:   sessionID,
		userID:      userID,
		filename:    filename,
		size:        size,
		stagingPath: stagingPath,
		file:        f,
		updated:     time.Now(),
	}

	m.mu.Lock()
	if m.pendingUploads == nil {
		m.pendingUploads = map[string]*pendingUpload{}
	}
	m.pendingUploads[uploadID] = p
	m.mu.Unlock()

	return uploadID, nil
}

// ErrChunkOutOfOrder signals that the offset on a WriteChunk doesn't
// match the upload's current cursor. Surface as 409 to the client so
// it can fetch upload status and retry the missing range.
var ErrChunkOutOfOrder = errors.New("chunk out of order")

// WriteChunk appends bytes from body to the upload's staging file at
// offset. The chunk's `total` (the third value in a Content-Range
// header) must match the size declared at BeginUpload — the server
// won't extend a file mid-stream.
//
// Returns the new total bytes written and complete=true once the file
// is fully assembled. complete=false plus a nil error means more
// chunks are expected.
func (m *Manager) WriteChunk(userID, sessionID, uploadID string, offset, total, chunkLen int64, body io.Reader) (written int64, complete bool, err error) {
	p, err := m.lookupUpload(userID, sessionID, uploadID)
	if err != nil {
		return 0, false, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if total != p.size {
		return p.written, false, fmt.Errorf("declared total %d does not match upload size %d", total, p.size)
	}
	if offset != p.written {
		return p.written, false, fmt.Errorf("%w: expected offset %d, got %d", ErrChunkOutOfOrder, p.written, offset)
	}
	if offset+chunkLen > p.size {
		return p.written, false, fmt.Errorf("chunk would exceed declared size")
	}

	// Read at most chunkLen+1 bytes so an oversized body fails fast
	// (and we never write past the declared size into staging).
	n, err := io.Copy(p.file, io.LimitReader(body, chunkLen+1))
	if err != nil {
		return p.written + n, false, fmt.Errorf("write chunk: %w", err)
	}
	if n != chunkLen {
		return p.written + n, false, fmt.Errorf("chunk length mismatch: header said %d, body had %d", chunkLen, n)
	}

	p.written += n
	p.updated = time.Now()
	return p.written, p.written == p.size, nil
}

// FinalizeUpload closes the staging file, parses tags, plans the
// destination, and appends the resulting Item to the session. Returns
// the item so the client can pick conflict/missing flags off it for
// the review UI.
func (m *Manager) FinalizeUpload(userID, sessionID, uploadID string) (*Item, error) {
	p, err := m.lookupUpload(userID, sessionID, uploadID)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.written != p.size {
		return nil, fmt.Errorf("upload incomplete: %d/%d bytes", p.written, p.size)
	}
	if err := p.file.Close(); err != nil {
		return nil, fmt.Errorf("close staging file: %w", err)
	}
	p.file = nil

	tags, tagErr := readTagsFromFile(p.stagingPath, strings.ToLower(filepath.Ext(p.filename)), p.filename)
	plan := PlanDestination(tags)

	item := &Item{
		ID:           p.uploadID,
		OriginalName: p.filename,
		SizeBytes:    p.size,
		Status:       ItemStatusStaged,
		Plan:         plan,
		stagingPath:  p.stagingPath,
	}
	if tagErr != nil {
		item.Error = "tag parse: " + tagErr.Error()
	}

	// Conflict detection mirrors AddItem: same destination as another
	// (non-skipped) item in this session, OR a real file already at
	// the planned location.
	finalAbs := filepath.Join(m.musicDir, plan.RelPath)

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok || s.UserID != userID {
		// Session disappeared between chunk uploads and finalize.
		// Best-effort cleanup of the now-orphaned staged file.
		os.Remove(p.stagingPath)
		delete(m.pendingUploads, uploadID)
		return nil, fmt.Errorf("session not found")
	}

	for _, existing := range s.Items {
		if existing.Status == ItemStatusSkipped {
			continue
		}
		if existing.Plan.RelPath == plan.RelPath {
			item.Status = ItemStatusConflict
			break
		}
	}
	if item.Status == ItemStatusStaged {
		if _, err := os.Stat(finalAbs); err == nil {
			item.Status = ItemStatusConflict
		}
	}

	s.Items = append(s.Items, item)
	s.UpdatedAt = time.Now()
	delete(m.pendingUploads, uploadID)

	return item, nil
}

// AbortUpload discards a pending upload and its staged bytes. Idempotent:
// calling on a finalized or unknown upload is a no-op.
func (m *Manager) AbortUpload(userID, sessionID, uploadID string) {
	m.mu.Lock()
	p, ok := m.pendingUploads[uploadID]
	if !ok || p.sessionID != sessionID || p.userID != userID {
		m.mu.Unlock()
		return
	}
	delete(m.pendingUploads, uploadID)
	m.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file != nil {
		p.file.Close()
		p.file = nil
	}
	os.Remove(p.stagingPath)
}

// lookupUpload returns the pending upload owned by userID/sessionID
// or an error if it doesn't exist or is owned by a different user.
// Like GetSession, the userID check is mandatory.
func (m *Manager) lookupUpload(userID, sessionID, uploadID string) (*pendingUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pendingUploads[uploadID]
	if !ok {
		return nil, fmt.Errorf("upload not found")
	}
	if p.sessionID != sessionID || p.userID != userID {
		return nil, fmt.Errorf("upload not found")
	}
	return p, nil
}

// pendingForSession returns the pending uploads attached to a session.
// Caller must hold m.mu. Used to keep MaxSessionFiles honest across
// committed Items + uploads still streaming.
func (m *Manager) pendingForSession(sessionID string) []*pendingUpload {
	out := make([]*pendingUpload, 0)
	for _, p := range m.pendingUploads {
		if p.sessionID == sessionID {
			out = append(out, p)
		}
	}
	return out
}
