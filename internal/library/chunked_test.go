package library

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkedUpload_SingleChunkFinalizes(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	s, _ := mgr.CreateSession("user-1")

	payload := []byte("not-real-audio-but-the-planner-falls-back-to-filename")
	uploadID, err := mgr.BeginUpload("user-1", s.ID, "song.flac", int64(len(payload)))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}

	written, complete, err := mgr.WriteChunk("user-1", s.ID, uploadID, 0, int64(len(payload)), int64(len(payload)), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if !complete || written != int64(len(payload)) {
		t.Fatalf("complete=%v written=%d, want complete=true written=%d", complete, written, len(payload))
	}

	item, err := mgr.FinalizeUpload("user-1", s.ID, uploadID)
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}
	if item.Status != ItemStatusStaged {
		t.Fatalf("status = %s, want staged", item.Status)
	}
	if item.SizeBytes != int64(len(payload)) {
		t.Fatalf("SizeBytes = %d, want %d", item.SizeBytes, len(payload))
	}
}

func TestChunkedUpload_MultipleChunksAssembleInOrder(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	s, _ := mgr.CreateSession("user-1")

	// Two chunks. Total payload distinguishes positions so we can
	// assert the staging file ends up in the right order.
	chunkA := bytes.Repeat([]byte("A"), 100)
	chunkB := bytes.Repeat([]byte("B"), 50)
	total := int64(len(chunkA) + len(chunkB))

	uploadID, err := mgr.BeginUpload("user-1", s.ID, "song.flac", total)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}

	if _, _, err := mgr.WriteChunk("user-1", s.ID, uploadID, 0, total, int64(len(chunkA)), bytes.NewReader(chunkA)); err != nil {
		t.Fatalf("WriteChunk A: %v", err)
	}
	if _, complete, err := mgr.WriteChunk("user-1", s.ID, uploadID, int64(len(chunkA)), total, int64(len(chunkB)), bytes.NewReader(chunkB)); err != nil || !complete {
		t.Fatalf("WriteChunk B: complete=%v err=%v", complete, err)
	}

	item, err := mgr.FinalizeUpload("user-1", s.ID, uploadID)
	if err != nil {
		t.Fatalf("FinalizeUpload: %v", err)
	}

	// Assemble + finalize moved the bytes; verify the staging file
	// content is exactly chunkA||chunkB.
	got, err := os.ReadFile(filepath.Join(dir, item.Plan.RelPath))
	if err != nil {
		// File hasn't been committed yet — read from staging instead.
		got, err = os.ReadFile(item.stagingPath)
		if err != nil {
			t.Fatalf("read staged file: %v", err)
		}
	}
	want := append(append([]byte{}, chunkA...), chunkB...)
	if !bytes.Equal(got, want) {
		t.Fatalf("staged bytes mismatch (len got=%d want=%d)", len(got), len(want))
	}
}

func TestChunkedUpload_OutOfOrderRejected(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	uploadID, _ := mgr.BeginUpload("user-1", s.ID, "song.flac", 200)

	// Skip ahead — writing at offset 100 before offset 0.
	chunk := bytes.Repeat([]byte("X"), 100)
	_, _, err := mgr.WriteChunk("user-1", s.ID, uploadID, 100, 200, 100, bytes.NewReader(chunk))
	if !errors.Is(err, ErrChunkOutOfOrder) {
		t.Fatalf("WriteChunk err = %v, want ErrChunkOutOfOrder", err)
	}
}

func TestChunkedUpload_SizeMismatchRejected(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	uploadID, _ := mgr.BeginUpload("user-1", s.ID, "song.flac", 200)

	// Header total disagrees with what BeginUpload was told.
	chunk := bytes.Repeat([]byte("X"), 50)
	if _, _, err := mgr.WriteChunk("user-1", s.ID, uploadID, 0, 999, 50, bytes.NewReader(chunk)); err == nil {
		t.Fatalf("expected size-mismatch error")
	}
}

func TestChunkedUpload_FinalizeBeforeCompleteFails(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	uploadID, _ := mgr.BeginUpload("user-1", s.ID, "song.flac", 200)

	chunk := bytes.Repeat([]byte("X"), 50)
	if _, _, err := mgr.WriteChunk("user-1", s.ID, uploadID, 0, 200, 50, bytes.NewReader(chunk)); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if _, err := mgr.FinalizeUpload("user-1", s.ID, uploadID); err == nil {
		t.Fatalf("expected error finalising incomplete upload")
	}
}

func TestChunkedUpload_AbortRemovesStaging(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	uploadID, _ := mgr.BeginUpload("user-1", s.ID, "song.flac", 50)

	chunk := bytes.Repeat([]byte("X"), 25)
	mgr.WriteChunk("user-1", s.ID, uploadID, 0, 50, 25, bytes.NewReader(chunk))

	mgr.AbortUpload("user-1", s.ID, uploadID)

	if _, err := mgr.lookupUpload("user-1", s.ID, uploadID); err == nil {
		t.Fatalf("expected upload to be gone after abort")
	}
}

func TestChunkedUpload_RejectsUnsupportedExtension(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")

	if _, err := mgr.BeginUpload("user-1", s.ID, "cover.jpg", 10); err == nil {
		t.Fatalf("expected error for unsupported extension")
	}
}

func TestChunkedUpload_CrossUserAccessRejected(t *testing.T) {
	mgr := NewManager(t.TempDir())
	s, _ := mgr.CreateSession("user-1")
	uploadID, _ := mgr.BeginUpload("user-1", s.ID, "song.flac", 50)

	if _, _, err := mgr.WriteChunk("user-2", s.ID, uploadID, 0, 50, 25, bytes.NewReader(make([]byte, 25))); err == nil {
		t.Fatalf("expected cross-user write to fail")
	}
}
