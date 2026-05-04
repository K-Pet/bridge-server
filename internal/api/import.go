package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/bridgemusic/bridge-server/internal/auth"
	"github.com/bridgemusic/bridge-server/internal/library"
	"github.com/bridgemusic/bridge-server/internal/navidrome"
)

// Import endpoints expose a session-based upload flow. Files are
// uploaded in chunks (Content-Range PUT) so a single request never
// hits the proxy/edge body-size cap (Cloudflare's free plan tops out
// at 100 MiB) or per-request stream timeouts.
//
//   POST   /api/library/import/sessions
//          → 201 {id, status:"open"}
//
//   POST   /api/library/import/sessions/{id}/uploads
//          {"filename":"track.flac","size":<bytes>}
//          → 201 {upload_id, chunk_size}
//
//   PUT    /api/library/import/sessions/{id}/uploads/{uploadId}
//          Content-Range: bytes <start>-<end>/<total>
//          body: raw chunk bytes
//          → 200 {bytes_written, complete}
//
//   POST   /api/library/import/sessions/{id}/uploads/{uploadId}/finalize
//          → 200 {item}
//
//   DELETE /api/library/import/sessions/{id}/uploads/{uploadId}
//          → 204
//
//   GET    /api/library/import/sessions/{id}
//          → 200 {session with all items + plans}
//
//   POST   /api/library/import/sessions/{id}/commit
//          optional body {allow_overwrite:true}
//          → 200 {result}, triggers Navidrome scan in background
//
//   DELETE /api/library/import/sessions/{id}
//          → 204, removes staging dir
//
//   POST   /api/library/import/sessions/{id}/items/{itemId}/skip
//          → 200, marks item skipped (no commit)
//
// Per-user scoping is enforced inside library.Manager — every call
// passes the auth context's userID and a session bound to a different
// user surfaces as 404.

func handleCreateImportSession(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s, err := mgr.CreateSession(userID)
		if err != nil {
			slog.Error("import: create session failed", "user", userID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "session_create_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(s)
	}
}

// maxChunkOverhead bounds the slack we allow on a single chunk PUT
// over what the server advertises as DefaultChunkSize. A sensible
// client always stays within DefaultChunkSize; the headroom is just
// enough for "client wanted a slightly different boundary" without
// exposing the staging path to a single oversized stream.
const maxChunkOverhead = 4 * 1024 * 1024 // 4 MiB

func handleBeginUpload(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")

		var body struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_body", err.Error())
			return
		}
		if body.Filename == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_filename", "filename is required")
			return
		}

		uploadID, err := mgr.BeginUpload(userID, sessionID, body.Filename, body.Size)
		if err != nil {
			slog.Warn("import: begin upload failed", "user", userID, "session", sessionID, "name", body.Filename, "error", err)
			writeJSONError(w, http.StatusBadRequest, "begin_upload_failed", err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"upload_id":  uploadID,
			"chunk_size": library.DefaultChunkSize,
		})
	}
}

// chunkRangeRe parses an HTTP Content-Range header for a single byte
// range, e.g. "bytes 0-16777215/200000000". Strict format — the
// chunked client always sends this exact shape.
//
// Allocated as a function rather than a regex to dodge a per-call
// regex compile and to surface a cleaner error message.
func parseChunkRange(h string) (start, end, total int64, err error) {
	const prefix = "bytes "
	if !strings.HasPrefix(h, prefix) {
		return 0, 0, 0, errors.New(`expected Content-Range starting with "bytes "`)
	}
	body := h[len(prefix):]
	slash := strings.IndexByte(body, '/')
	if slash < 0 {
		return 0, 0, 0, errors.New("missing total in Content-Range")
	}
	rangeStr, totalStr := body[:slash], body[slash+1:]
	dash := strings.IndexByte(rangeStr, '-')
	if dash < 0 {
		return 0, 0, 0, errors.New("missing end in Content-Range")
	}
	start, err = strconv.ParseInt(rangeStr[:dash], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad start: %w", err)
	}
	end, err = strconv.ParseInt(rangeStr[dash+1:], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad end: %w", err)
	}
	total, err = strconv.ParseInt(totalStr, 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad total: %w", err)
	}
	if start < 0 || end < start || total < end+1 {
		return 0, 0, 0, errors.New("invalid range bounds")
	}
	return start, end, total, nil
}

func handleWriteChunk(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		uploadID := r.PathValue("uploadId")

		start, end, total, err := parseChunkRange(r.Header.Get("Content-Range"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_content_range", err.Error())
			return
		}
		chunkLen := end - start + 1

		// Cap the request body well above one chunk so an oversized
		// chunk fails before it can blow disk. WriteChunk also
		// validates chunk length matches the header.
		r.Body = http.MaxBytesReader(w, r.Body, library.DefaultChunkSize+maxChunkOverhead)

		written, complete, err := mgr.WriteChunk(userID, sessionID, uploadID, start, total, chunkLen, r.Body)
		if err != nil {
			status := http.StatusBadRequest
			code := "chunk_failed"
			if errors.Is(err, library.ErrChunkOutOfOrder) {
				// 409 Conflict tells the client to GET the upload
				// status (or re-do BeginUpload) and resume from the
				// server's known offset.
				status = http.StatusConflict
				code = "chunk_out_of_order"
			}
			slog.Warn("import: write chunk failed",
				"user", userID, "session", sessionID, "upload", uploadID,
				"start", start, "end", end, "total", total, "error", err)
			writeJSONError(w, status, code, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"bytes_written": written,
			"complete":      complete,
		})
	}
}

func handleFinalizeUpload(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		uploadID := r.PathValue("uploadId")

		item, err := mgr.FinalizeUpload(userID, sessionID, uploadID)
		if err != nil {
			slog.Warn("import: finalize upload failed", "user", userID, "session", sessionID, "upload", uploadID, "error", err)
			writeJSONError(w, http.StatusBadRequest, "finalize_failed", err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(item)
	}
}

func handleAbortUpload(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		uploadID := r.PathValue("uploadId")
		mgr.AbortUpload(userID, sessionID, uploadID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleGetImportSession(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		s, err := mgr.GetSession(userID, sessionID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "Session not found.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	}
}

func handleSkipImportItem(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		itemID := r.PathValue("itemId")
		if err := mgr.SkipItem(userID, sessionID, itemID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "skip_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleCommitImportSession(mgr *library.Manager, nd *navidrome.Client, hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")

		// Body is optional — empty body means default options.
		var body struct {
			AllowOverwrite bool `json:"allow_overwrite"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONError(w, http.StatusBadRequest, "bad_body", err.Error())
				return
			}
		}

		res, err := mgr.Commit(userID, sessionID, body.AllowOverwrite)
		if err != nil {
			slog.Error("import: commit failed", "user", userID, "session", sessionID, "error", err)
			writeJSONError(w, http.StatusBadRequest, "commit_failed", err.Error())
			return
		}

		// Trigger Navidrome scan in the background — same pattern as
		// the delete handlers. SSE event lets the frontend refresh
		// the library view as soon as the new files appear.
		if nd != nil {
			go func() {
				ctx := context.Background()
				if err := nd.StartScan(ctx); err != nil {
					slog.Error("import: post-commit scan failed", "session", sessionID, "error", err)
				}
				if hub != nil {
					hub.Publish(Event{
						Type: "library_updated",
						Data: map[string]any{
							"import_session": sessionID,
							"committed":      len(res.Committed),
						},
					})
				}
			}()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"committed": res.Committed,
			"skipped":   res.Skipped,
			"failed":    res.Failed,
			"scanning":  nd != nil,
		})
	}
}

func handleAbortImportSession(mgr *library.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserID(r.Context())
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionID := r.PathValue("id")
		if err := mgr.Abort(userID, sessionID); err != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
