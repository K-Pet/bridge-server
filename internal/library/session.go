package library

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dhowden/tag"
)

// MaxUploadBytes caps a single uploaded audio file. Albums full of
// hi-res FLAC easily hit a couple hundred MB; lossless live concert
// boots can run larger. 1 GiB per file is generous enough to cover
// real music while still bounding the disk we'll commit to a single
// session (see also MaxSessionFiles).
const MaxUploadBytes = 1 << 30 // 1 GiB

// MaxSessionFiles caps how many tracks a single import session can
// hold. Two purposes:
//   - keep the in-memory session struct bounded
//   - put a soft ceiling on staged disk usage per user before the
//     frontend has to start a second session
//
// 50k covers even fanatical personal libraries in a single session.
// The session struct is ~200 bytes per item plus the staged file on
// disk, so 50k × 200B ≈ 10 MiB of RAM in the worst case — fine. If
// a user actually hits this they have bigger problems than a session
// boundary.
const MaxSessionFiles = 50_000

// SessionTTL is how long a session can sit untouched in the manager
// before the janitor reaps it (and removes its staging directory).
// Picked to span an overnight upload from a flaky connection without
// being so long that abandoned sessions chew disk indefinitely.
const SessionTTL = 24 * time.Hour

// ItemStatus tracks the lifecycle of a single uploaded file inside a
// session. Files don't move backward — once committed (or skipped),
// they stay that way for the life of the session.
type ItemStatus string

const (
	ItemStatusStaged    ItemStatus = "staged"    // uploaded + tags read, awaiting commit
	ItemStatusConflict  ItemStatus = "conflict"  // destination already exists; needs resolution
	ItemStatusCommitted ItemStatus = "committed" // moved into final library path
	ItemStatusSkipped   ItemStatus = "skipped"   // user-removed from session before commit
	ItemStatusFailed    ItemStatus = "failed"    // upload or commit failed
)

// Item is a single file inside an import session: the file the user
// uploaded, the destination we plan to write it to, and any
// quality/conflict flags the UI needs to present a review screen.
type Item struct {
	ID           string     `json:"id"`
	OriginalName string     `json:"original_name"`        // filename as the client uploaded it (display only)
	SizeBytes    int64      `json:"size_bytes"`
	Status       ItemStatus `json:"status"`
	Plan         Plan       `json:"plan"`
	Error        string     `json:"error,omitempty"`

	// stagingPath is the absolute on-disk path of the staged upload.
	// Kept private so callers can't mutate it; commit reads it under
	// the manager lock and then renames atomically.
	stagingPath string
}

// SessionStatus is the high-level state of an import session. Like
// item statuses, transitions are forward-only.
type SessionStatus string

const (
	SessionStatusOpen       SessionStatus = "open" // accepting uploads
	SessionStatusCommitting SessionStatus = "committing"
	SessionStatusComplete   SessionStatus = "complete"
)

// Session is one user's import attempt: a staging directory on
// disk, a list of items uploaded so far, and the state machine
// described by SessionStatus. Always accessed via the Manager,
// which owns the mutex.
type Session struct {
	ID         string        `json:"id"`
	UserID     string        `json:"user_id"`
	Status     SessionStatus `json:"status"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Items      []*Item       `json:"items"`

	// stagingDir is the per-session folder under StagingRoot. Cleared
	// on commit/abort so we don't leak disk to abandoned uploads.
	stagingDir string
}

// Manager owns the in-memory session table and the on-disk staging
// area. Safe for concurrent use; one mutex guards both the session
// map and per-session item lists (uploads to the same session are
// rare enough that fine-grained locking isn't worth the complexity).
type Manager struct {
	musicDir string
	mu       sync.Mutex
	sessions map[string]*Session

	// pendingUploads tracks chunked-upload state for files mid-flight.
	// Keyed by uploadID; populated by BeginUpload, drained by
	// FinalizeUpload/AbortUpload, and reaped alongside stale sessions.
	pendingUploads map[string]*pendingUpload
}

// NewManager returns a Manager rooted at musicDir. The staging
// directory is created lazily (on first session create) so we don't
// touch the filesystem during boot if no one ever imports.
func NewManager(musicDir string) *Manager {
	return &Manager{
		musicDir:       musicDir,
		sessions:       map[string]*Session{},
		pendingUploads: map[string]*pendingUpload{},
	}
}

// CreateSession allocates a new session for userID, mints a session
// id, and prepares its staging directory under <musicDir>/.import-staging/<user>/<id>.
func (m *Manager) CreateSession(userID string) (*Session, error) {
	if userID == "" {
		return nil, fmt.Errorf("user id required")
	}
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("session id: %w", err)
	}

	stagingDir := filepath.Join(m.musicDir, StagingRoot, sanitizeForPath(userID), id)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	now := time.Now()
	s := &Session{
		ID:         id,
		UserID:     userID,
		Status:     SessionStatusOpen,
		CreatedAt:  now,
		UpdatedAt:  now,
		Items:      []*Item{},
		stagingDir: stagingDir,
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// GetSession returns the session for id, or an error if it doesn't
// exist or belongs to a different user. Caller-supplied userID
// scoping is mandatory — sessions are per-user and there's no admin
// override; mixing them up would let one user see another's queued
// uploads.
func (m *Manager) GetSession(userID, id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	if s.UserID != userID {
		return nil, fmt.Errorf("session not found")
	}
	return s, nil
}

// AddItem streams body into the session's staging dir, reads tags,
// builds a Plan, and appends the Item to the session. Returns the
// new item or an error suitable for surfacing to the client.
//
// Caller is responsible for closing body. The size limit is enforced
// here (not by the HTTP handler) so the manager can guarantee no
// session ever holds an oversized file regardless of how the upload
// arrived.
func (m *Manager) AddItem(userID, sessionID, originalName string, body io.Reader) (*Item, error) {
	s, err := m.GetSession(userID, sessionID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if s.Status != SessionStatusOpen {
		m.mu.Unlock()
		return nil, fmt.Errorf("session not accepting uploads (status=%s)", s.Status)
	}
	if len(s.Items) >= MaxSessionFiles {
		m.mu.Unlock()
		return nil, fmt.Errorf("session full (max %d files)", MaxSessionFiles)
	}
	m.mu.Unlock()

	ext := strings.ToLower(filepath.Ext(originalName))
	if !IsSupportedAudio(ext) {
		return nil, fmt.Errorf("unsupported file type %q", ext)
	}

	itemID, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("item id: %w", err)
	}
	stagingPath := filepath.Join(s.stagingDir, itemID+ext)

	written, err := writeLimited(stagingPath, body, MaxUploadBytes)
	if err != nil {
		os.Remove(stagingPath)
		return nil, fmt.Errorf("write upload: %w", err)
	}

	tags, tagErr := readTagsFromFile(stagingPath, ext, originalName)
	plan := PlanDestination(tags)

	item := &Item{
		ID:           itemID,
		OriginalName: originalName,
		SizeBytes:    written,
		Status:       ItemStatusStaged,
		Plan:         plan,
		stagingPath:  stagingPath,
	}
	if tagErr != nil {
		// Bad/missing tags don't fail the upload — the planner already
		// fell back to "Unknown". Surface the parse error so the UI can
		// show a hint, but keep the item.
		item.Error = "tag parse: " + tagErr.Error()
	}

	// Conflict detection: the destination already exists on disk, OR
	// another item in this same session is planning to write there.
	// In either case the UI should prompt for rename/skip before commit.
	finalAbs := filepath.Join(m.musicDir, plan.RelPath)

	m.mu.Lock()
	defer m.mu.Unlock()
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
	return item, nil
}

// SkipItem marks an item as skipped so it won't be committed. The
// staged file is removed immediately to free disk; the item record
// is kept so the UI can still show what was on the upload list.
func (m *Manager) SkipItem(userID, sessionID, itemID string) error {
	s, err := m.GetSession(userID, sessionID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range s.Items {
		if it.ID != itemID {
			continue
		}
		if it.Status == ItemStatusCommitted {
			return fmt.Errorf("item already committed")
		}
		if it.stagingPath != "" {
			os.Remove(it.stagingPath)
		}
		it.Status = ItemStatusSkipped
		s.UpdatedAt = time.Now()
		return nil
	}
	return fmt.Errorf("item not found")
}

// CommitResult summarizes the outcome of a Commit call.
type CommitResult struct {
	Committed []string `json:"committed"` // item IDs successfully moved
	Skipped   []string `json:"skipped"`   // item IDs in skipped/conflict states (not moved)
	Failed    []string `json:"failed"`    // item IDs that errored during the move
}

// Commit moves every staged item in the session into its planned
// final location under the music root. Items in conflict or skipped
// states are left alone. After the move the staging dir is cleaned
// up and the session marked complete. Caller should then trigger a
// Navidrome scan so the new files appear in the library.
//
// allowOverwrite=true causes Commit to also process items currently
// in conflict status, overwriting whatever sits at the destination.
// Default (false) leaves them in the result's Skipped list.
func (m *Manager) Commit(userID, sessionID string, allowOverwrite bool) (*CommitResult, error) {
	s, err := m.GetSession(userID, sessionID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if s.Status != SessionStatusOpen {
		m.mu.Unlock()
		return nil, fmt.Errorf("session not committable (status=%s)", s.Status)
	}
	s.Status = SessionStatusCommitting
	items := append([]*Item(nil), s.Items...) // snapshot
	m.mu.Unlock()

	res := &CommitResult{}
	for _, it := range items {
		// Items added to the session are either Staged (commit) or
		// Conflict (commit only on overwrite); Skipped items skip; the
		// other terminal states (Committed/Failed) can't appear here
		// because they're set inside this loop and the session resets
		// after Commit returns.
		if it.Status == ItemStatusSkipped {
			res.Skipped = append(res.Skipped, it.ID)
			continue
		}
		if it.Status == ItemStatusConflict && !allowOverwrite {
			res.Skipped = append(res.Skipped, it.ID)
			continue
		}

		finalAbs := filepath.Join(m.musicDir, it.Plan.RelPath)
		if err := os.MkdirAll(filepath.Dir(finalAbs), 0o755); err != nil {
			m.markItem(s, it.ID, ItemStatusFailed, "mkdir: "+err.Error())
			res.Failed = append(res.Failed, it.ID)
			continue
		}
		if err := os.Rename(it.stagingPath, finalAbs); err != nil {
			m.markItem(s, it.ID, ItemStatusFailed, "rename: "+err.Error())
			res.Failed = append(res.Failed, it.ID)
			continue
		}
		m.markItem(s, it.ID, ItemStatusCommitted, "")
		res.Committed = append(res.Committed, it.ID)
	}

	// Best-effort staging cleanup. Anything that errored above stays
	// behind for the janitor to mop up via session TTL.
	os.RemoveAll(s.stagingDir)

	m.mu.Lock()
	s.Status = SessionStatusComplete
	s.UpdatedAt = time.Now()
	m.mu.Unlock()

	slog.Info("library import committed",
		"session", sessionID,
		"user", userID,
		"committed", len(res.Committed),
		"skipped", len(res.Skipped),
		"failed", len(res.Failed))
	return res, nil
}

// Abort discards a session: removes the staging directory, drops any
// pending chunked uploads attached to it, and clears the session from
// memory. Safe to call on already-complete sessions (it just becomes
// a memory cleanup).
func (m *Manager) Abort(userID, sessionID string) error {
	s, err := m.GetSession(userID, sessionID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, sessionID)
	dir := s.stagingDir
	pending := []*pendingUpload{}
	for id, p := range m.pendingUploads {
		if p.sessionID == sessionID {
			pending = append(pending, p)
			delete(m.pendingUploads, id)
		}
	}
	m.mu.Unlock()

	for _, p := range pending {
		p.mu.Lock()
		if p.file != nil {
			p.file.Close()
			p.file = nil
		}
		p.mu.Unlock()
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
	return nil
}

// ReapStale removes sessions that haven't been touched in TTL and
// drops their staging directories. Pending chunked uploads attached
// to those sessions are dropped too — the staging dir removal pulls
// their on-disk bytes; the in-memory state is cleared here. Pending
// uploads not attached to any session (orphans from a session that
// vanished mid-upload) get their own age-based reap pass.
//
// Intended to run on a periodic timer from main; safe to call
// concurrently with normal session activity.
func (m *Manager) ReapStale(now time.Time) int {
	m.mu.Lock()
	stale := []*Session{}
	for id, s := range m.sessions {
		if now.Sub(s.UpdatedAt) > SessionTTL {
			stale = append(stale, s)
			delete(m.sessions, id)
		}
	}
	staleSessionIDs := map[string]struct{}{}
	for _, s := range stale {
		staleSessionIDs[s.ID] = struct{}{}
	}
	staleUploads := []*pendingUpload{}
	for id, p := range m.pendingUploads {
		_, sessionGone := staleSessionIDs[p.sessionID]
		if sessionGone || now.Sub(p.updated) > pendingUploadTTL {
			staleUploads = append(staleUploads, p)
			delete(m.pendingUploads, id)
		}
	}
	m.mu.Unlock()

	for _, p := range staleUploads {
		p.mu.Lock()
		if p.file != nil {
			p.file.Close()
			p.file = nil
		}
		os.Remove(p.stagingPath)
		p.mu.Unlock()
	}
	for _, s := range stale {
		if s.stagingDir != "" {
			os.RemoveAll(s.stagingDir)
		}
		slog.Info("library import session reaped", "session", s.ID, "user", s.UserID, "age", now.Sub(s.UpdatedAt))
	}
	return len(stale)
}

func (m *Manager) markItem(s *Session, itemID string, status ItemStatus, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range s.Items {
		if it.ID == itemID {
			it.Status = status
			if errMsg != "" {
				it.Error = errMsg
			}
			return
		}
	}
}

// readTagsFromFile parses ID3/Vorbis/MP4 tags from a staged file.
// On error the returned TrackTags has only the extension and a
// title-from-filename fallback so the planner still has something to
// route on. The error itself is returned so the upload handler can
// expose "tags unreadable" in the item record.
func readTagsFromFile(path, ext, originalName string) (TrackTags, error) {
	tags := TrackTags{Extension: ext}
	titleFromName := strings.TrimSuffix(filepath.Base(originalName), filepath.Ext(originalName))
	tags.Title = titleFromName

	f, err := os.Open(path)
	if err != nil {
		return tags, err
	}
	defer f.Close()

	parsed, err := tag.ReadFrom(f)
	if err != nil {
		return tags, err
	}

	if v := strings.TrimSpace(parsed.Title()); v != "" {
		tags.Title = v
	}
	tags.Artist = strings.TrimSpace(parsed.Artist())
	tags.AlbumArtist = strings.TrimSpace(parsed.AlbumArtist())
	tags.Album = strings.TrimSpace(parsed.Album())

	tags.TrackNumber, _ = parsed.Track()
	tags.DiscNumber, tags.DiscTotal = parsed.Disc()
	return tags, nil
}

// writeLimited streams body into path, bailing with an error if the
// total exceeds limit. Returns bytes written. On any error the
// partial file is *not* removed by this function — the caller knows
// where it lives and decides how to clean up.
func writeLimited(path string, body io.Reader, limit int64) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// LimitReader at limit+1 lets us detect "exceeded" without ever
	// actually copying past the cap into our destination file.
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	return n, nil
}

// randomID returns a 16-byte hex string suitable for use as a
// session or item id. crypto/rand keeps these unguessable so a
// session id alone is enough auth to access a session (combined
// with the userID check inside GetSession).
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sanitizeForPath strips characters from a userID that would mess
// with the filesystem, used only for the staging-dir path component.
// Doesn't go through Sanitize because we want to preserve the full
// id (UUIDs are fine) without the "Unknown" fallback semantics.
func sanitizeForPath(s string) string {
	s = unsafePathChars.ReplaceAllString(s, "_")
	s = strings.Trim(s, ". ")
	if s == "" {
		s = "anon"
	}
	return s
}
