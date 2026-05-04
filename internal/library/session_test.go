package library

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeAudioBytes returns a minimal byte payload that is *not* a valid
// audio file — the tag parser will fail, which exercises the "tag
// parse error → fall back to filename" branch in AddItem. Lets these
// tests exercise the session lifecycle without needing real fixtures.
func fakeAudioBytes() []byte {
	return []byte("not really audio, but the planner doesn't care for this test")
}

func TestSessionLifecycle_StagesUploadsAndCommits(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	s, err := mgr.CreateSession("user-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.Status != SessionStatusOpen {
		t.Fatalf("status = %s, want open", s.Status)
	}

	item, err := mgr.AddItem("user-1", s.ID, "song.flac", bytes.NewReader(fakeAudioBytes()))
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if item.Status != ItemStatusStaged {
		t.Fatalf("item status = %s, want staged", item.Status)
	}
	// Tag parse will have failed on our fake bytes, so the planner
	// fell back to filename → Title="song", and Missing flags fire.
	if !item.Plan.MissingArtist || !item.Plan.MissingAlbum {
		t.Fatalf("expected Missing flags on tagless upload, got %+v", item.Plan)
	}
	if !filepath.IsAbs(item.stagingPath) {
		t.Fatalf("expected absolute staging path, got %q", item.stagingPath)
	}
	if _, err := os.Stat(item.stagingPath); err != nil {
		t.Fatalf("staging file missing: %v", err)
	}

	res, err := mgr.Commit("user-1", s.ID, false)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(res.Committed) != 1 {
		t.Fatalf("Committed = %v, want 1", res.Committed)
	}

	finalPath := filepath.Join(dir, item.Plan.RelPath)
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final file missing at %s: %v", finalPath, err)
	}
	// Staging dir should be cleaned up.
	if _, err := os.Stat(s.stagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging dir still exists after commit: %v", err)
	}
}

func TestAddItem_RejectsUnsupportedExtension(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")

	_, err := mgr.AddItem("user-1", s.ID, "cover.jpg", bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}

func TestGetSession_RejectsCrossUserAccess(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")

	if _, err := mgr.GetSession("user-2", s.ID); err == nil {
		t.Fatalf("expected cross-user lookup to fail")
	}
}

func TestAddItem_DetectsIntraSessionConflict(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")

	first, err := mgr.AddItem("user-1", s.ID, "track.flac", bytes.NewReader(fakeAudioBytes()))
	if err != nil {
		t.Fatalf("AddItem 1: %v", err)
	}
	second, err := mgr.AddItem("user-1", s.ID, "track.flac", bytes.NewReader(fakeAudioBytes()))
	if err != nil {
		t.Fatalf("AddItem 2: %v", err)
	}
	if first.Status != ItemStatusStaged {
		t.Fatalf("first status = %s, want staged", first.Status)
	}
	if second.Status != ItemStatusConflict {
		t.Fatalf("second status = %s, want conflict (same planned destination as first)", second.Status)
	}
}

func TestAddItem_DetectsExistingFileConflict(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	s, _ := mgr.CreateSession("user-1")

	// Pre-stage a fake destination so AddItem flags it as a conflict.
	plan := PlanDestination(TrackTags{
		Title:     "song",
		Extension: ".flac",
	})
	target := filepath.Join(dir, plan.RelPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	item, err := mgr.AddItem("user-1", s.ID, "song.flac", bytes.NewReader(fakeAudioBytes()))
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if item.Status != ItemStatusConflict {
		t.Fatalf("status = %s, want conflict", item.Status)
	}
}

func TestSkipItem_RemovesStagingFile(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	item, _ := mgr.AddItem("user-1", s.ID, "x.flac", bytes.NewReader(fakeAudioBytes()))

	if err := mgr.SkipItem("user-1", s.ID, item.ID); err != nil {
		t.Fatalf("SkipItem: %v", err)
	}
	if _, err := os.Stat(item.stagingPath); !os.IsNotExist(err) {
		t.Fatalf("staging file still present after skip: %v", err)
	}
	if item.Status != ItemStatusSkipped {
		t.Fatalf("status = %s, want skipped", item.Status)
	}
}

func TestAbort_RemovesStagingDir(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	mgr.AddItem("user-1", s.ID, "x.flac", bytes.NewReader(fakeAudioBytes()))

	if err := mgr.Abort("user-1", s.ID); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := os.Stat(s.stagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging dir still exists after abort: %v", err)
	}
	if _, err := mgr.GetSession("user-1", s.ID); err == nil {
		t.Fatalf("expected session to be gone")
	}
}

func TestReapStale_DropsOldSessions(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")

	// Force the session to look stale.
	mgr.mu.Lock()
	s.UpdatedAt = time.Now().Add(-2 * SessionTTL)
	mgr.mu.Unlock()

	if got := mgr.ReapStale(time.Now()); got != 1 {
		t.Fatalf("ReapStale returned %d, want 1", got)
	}
	if _, err := mgr.GetSession("user-1", s.ID); err == nil {
		t.Fatalf("expected reaped session to be gone")
	}
}

func TestCommit_WithoutOverwriteSkipsConflicts(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	s, _ := mgr.CreateSession("user-1")

	first, _ := mgr.AddItem("user-1", s.ID, "track.flac", bytes.NewReader(fakeAudioBytes()))
	second, _ := mgr.AddItem("user-1", s.ID, "track.flac", bytes.NewReader(fakeAudioBytes()))

	res, err := mgr.Commit("user-1", s.ID, false)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(res.Committed) != 1 || res.Committed[0] != first.ID {
		t.Fatalf("Committed = %v, want [%s]", res.Committed, first.ID)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != second.ID {
		t.Fatalf("Skipped = %v, want [%s]", res.Skipped, second.ID)
	}
}
