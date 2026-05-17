// Library Import API client.
//
// The bridge-server import flow is session-based:
//   1. Create a session                       → POST /sessions
//   2. Begin a per-file chunked upload        → POST /sessions/{id}/uploads
//   3. Stream chunks (Content-Range PUTs)     → PUT  /sessions/{id}/uploads/{uploadId}
//   4. Finalize (server reads tags + plans)   → POST /sessions/{id}/uploads/{uploadId}/finalize
//   5. Fetch the planned layout               → GET  /sessions/{id}
//   6. (optional) skip individual items       → POST /sessions/{id}/items/{itemId}/skip
//   7. Commit (atomic move + scan)            → POST /sessions/{id}/commit
//   8. (or) abort, dropping staging           → DELETE /sessions/{id}
//
// Chunked uploads exist for two reasons: Cloudflare's free plan caps
// request bodies at 100 MiB (a single hi-res FLAC easily exceeds), and
// long-stream PUTs hit per-request edge timeouts. Splitting each file
// into ~16 MiB chunks dodges both. The server validates the chunk
// sequence with Content-Range and tracks per-upload progress, so a
// failed chunk can be retried without re-uploading the whole file.
//
// Auth uses the same Supabase JWT as the rest of the app, fetched at
// the moment of upload rather than once per session — large libraries
// can outlast the JWT TTL otherwise.

import { getSupabase } from './supabase'

async function authHeader(): Promise<string | null> {
  const sb = getSupabase()
  if (!sb) return null
  const { data } = await sb.auth.getSession()
  return data.session?.access_token ? `Bearer ${data.session.access_token}` : null
}

// ── Types (mirror internal/library models) ─────────────────────────────

export type ItemStatus = 'staged' | 'conflict' | 'committed' | 'skipped' | 'failed'
export type SessionStatus = 'open' | 'committing' | 'complete'

export interface TrackTags {
  Title: string
  Artist: string
  AlbumArtist: string
  Album: string
  TrackNumber: number
  DiscNumber: number
  DiscTotal: number
  Extension: string
}

export interface ImportPlan {
  RelPath: string
  Effective: TrackTags
  MissingTitle: boolean
  MissingArtist: boolean
  MissingAlbum: boolean
  MissingTrackNumber: boolean
}

export interface ImportItem {
  id: string
  original_name: string
  status: ItemStatus
  plan: ImportPlan
}

export interface ImportSession {
  id: string
  user_id: string
  status: SessionStatus
  created_at: string
  updated_at: string
  items: ImportItem[]
}

export interface CommitResult {
  committed: string[]
  skipped: string[]
  failed: string[]
  scanning: boolean
}

// ── HTTP wrappers ──────────────────────────────────────────────────────

async function jsonRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const auth = await authHeader()
  const headers: Record<string, string> = { ...(init?.headers as Record<string, string>) }
  if (auth) headers.Authorization = auth
  const res = await fetch(path, { ...init, headers })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`API ${res.status}: ${text}`)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export async function createImportSession(): Promise<ImportSession> {
  return jsonRequest<ImportSession>('/api/library/import/sessions', { method: 'POST' })
}

export async function getImportSession(sessionId: string): Promise<ImportSession> {
  return jsonRequest<ImportSession>(`/api/library/import/sessions/${encodeURIComponent(sessionId)}`)
}

export async function commitImportSession(
  sessionId: string,
  allowOverwrite: boolean,
): Promise<CommitResult> {
  return jsonRequest<CommitResult>(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/commit`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ allow_overwrite: allowOverwrite }),
    },
  )
}

export async function abortImportSession(sessionId: string): Promise<void> {
  await jsonRequest<void>(`/api/library/import/sessions/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE',
  })
}

export async function skipImportItem(sessionId: string, itemId: string): Promise<void> {
  await jsonRequest<void>(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/items/${encodeURIComponent(itemId)}/skip`,
    { method: 'POST' },
  )
}

// uploadImportFile drives a chunked upload of one file. The flow:
//
//   1. POST /sessions/{id}/uploads → upload_id, server-suggested chunk_size
//   2. PUT  /sessions/{id}/uploads/{uploadId} for each chunk in sequence,
//      with Content-Range. Each chunk is short-lived enough to escape
//      proxy stream timeouts and well under the 100 MiB Cloudflare cap.
//      Failed chunks retry with exponential backoff (up to 4 attempts);
//      on a sustained failure we GET upload status and resume from the
//      server's authoritative offset.
//   3. POST /sessions/{id}/uploads/{uploadId}/finalize → returns the
//      planned ImportItem (server reads tags + builds destination).
//
// Progress aggregates across chunks: `bytes` is the total transferred
// across the whole file, `percent` is `(bytes / file.size) * 100`.
// Cancellation via AbortSignal aborts the in-flight chunk and posts
// a best-effort DELETE so the server can drop staged bytes early.

export interface UploadCallbacks {
  signal?: AbortSignal
  onProgress?: (percent: number, bytes: number) => void
  // onUploadID fires the moment BeginUpload returns the server's
  // upload id. The queue stashes it on the QueuedFile so a manual
  // retry (after a hard failure) can resume against the same
  // server-side staging file rather than starting from byte 0.
  onUploadID?: (uploadID: string) => void
}

interface BeginUploadResponse {
  upload_id: string
  chunk_size: number
}

interface UploadStatusResponse {
  upload_id: string
  bytes_written: number
  size: number
}

// MAX_CHUNK_SIZE caps the per-request body even if the server suggests
// a larger one. 4 MiB lines up with the server's DefaultChunkSize and
// keeps each chunk's transmit time under Cloudflare's edge timeout
// even on a slow uplink (1–3 Mbit/s).
const MAX_CHUNK_SIZE = 4 * 1024 * 1024

// MAX_CHUNK_ATTEMPTS bounds the retry budget per chunk. 5 attempts
// with exponential backoff (0 / 500ms / 2s / 5s / 12s) cover most
// transient failures — Cloudflare 502/504, brief Wi-Fi blips, server
// momentarily busy — while still bailing fast enough on a sustained
// outage that the user gets a "failed, retry?" affordance within ~20 s.
// Anything longer than that and we'd be better off showing the
// manual Retry button so the user knows something's actually wrong.
const MAX_CHUNK_ATTEMPTS = 5

// chunkBackoffMs returns the wait before attempt N (0-indexed).
// First attempt is immediate; subsequent attempts back off but cap
// so the user isn't staring at a frozen progress bar for half a
// minute. Designed so 5 attempts cover ~20 s of trouble.
function chunkBackoffMs(attempt: number): number {
  if (attempt <= 0) return 0
  const schedule = [500, 2_000, 5_000, 12_000]
  return schedule[attempt - 1] ?? schedule[schedule.length - 1]
}

// CHUNK_TIMEOUT_MS bounds a single chunk PUT. XHR has no built-in
// timeout; a stalled TCP connection would hang forever otherwise.
// 60 s is generous for a 4 MiB chunk on any reasonable uplink and
// gives us a chance to retry inside Cloudflare's 100 s window.
const CHUNK_TIMEOUT_MS = 60_000

// UploadFailedError is thrown when the chunk-retry budget is
// exhausted. The uploadID is carried on the error so the caller can
// offer the user a "Retry" affordance and resume against the same
// server-side staging file rather than restarting from byte 0.
export class UploadFailedError extends Error {
  constructor(public uploadID: string, public cause: Error) {
    super(cause.message)
    this.name = 'UploadFailedError'
  }
}

// uploadImportFile begins a new chunked upload and pushes every
// chunk through. On the happy path it returns the planned ImportItem
// from /finalize. On hard failure it throws UploadFailedError
// carrying the server-side upload id — the staging file is left in
// place so resumeImportFile(...) can pick up where the failure
// stopped without re-uploading already-accepted bytes.
//
// The server's session janitor will eventually reap an abandoned
// upload after its TTL (24 h by default), so leaving things around
// on failure is bounded.
export async function uploadImportFile(
  sessionId: string,
  file: File,
  cb?: UploadCallbacks,
): Promise<ImportItem> {
  if (cb?.signal?.aborted) {
    throw new DOMException('upload aborted', 'AbortError')
  }

  const begin = await jsonRequest<BeginUploadResponse>(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filename: file.name, size: file.size }),
      signal: cb?.signal,
    },
  )

  cb?.onUploadID?.(begin.upload_id)
  return pushChunksAndFinalize(sessionId, begin.upload_id, begin.chunk_size, file, 0, cb)
}

// resumeImportFile picks up an existing upload by id and pushes the
// remaining chunks. Used by the UI's Retry button: rather than
// re-uploading a 50 MB FLAC from byte 0 after a transient failure,
// we ask the server how much it already has and skip ahead.
//
// If the upload no longer exists server-side (the janitor reaped it,
// or the session was discarded), this throws and the caller should
// fall back to uploadImportFile to start fresh.
export async function resumeImportFile(
  sessionId: string,
  uploadID: string,
  file: File,
  cb?: UploadCallbacks,
): Promise<ImportItem> {
  if (cb?.signal?.aborted) {
    throw new DOMException('upload aborted', 'AbortError')
  }
  const status = await jsonRequest<UploadStatusResponse>(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}`,
    { signal: cb?.signal },
  )
  if (status.size !== file.size) {
    // Size mismatch means the user is trying to resume a different
    // file under the same id — treat as unrecoverable, caller falls
    // back to a fresh upload.
    throw new Error(`upload size mismatch: server has ${status.size}, file is ${file.size}`)
  }
  // Surface the already-uploaded progress to the UI so the bar jumps
  // straight to where the server is, rather than starting at 0.
  cb?.onProgress?.(
    file.size === 0 ? 100 : Math.round((status.bytes_written / file.size) * 100),
    status.bytes_written,
  )
  return pushChunksAndFinalize(sessionId, uploadID, MAX_CHUNK_SIZE, file, status.bytes_written, cb)
}

// pushChunksAndFinalize is the shared loop used by both
// uploadImportFile (fresh start) and resumeImportFile (resume from
// `offset`). Centralizing it ensures the retry/error-tagging logic
// stays identical across both entry points.
async function pushChunksAndFinalize(
  sessionId: string,
  uploadID: string,
  serverChunkSize: number,
  file: File,
  offset: number,
  cb?: UploadCallbacks,
): Promise<ImportItem> {
  const chunkSize = Math.min(serverChunkSize || MAX_CHUNK_SIZE, MAX_CHUNK_SIZE)

  try {
    while (offset < file.size) {
      const end = Math.min(offset + chunkSize, file.size)
      offset = await sendChunkWithRetry(sessionId, uploadID, file, offset, end, cb)
      cb?.onProgress?.(file.size === 0 ? 100 : Math.round((offset / file.size) * 100), offset)
    }

    const item = await jsonRequest<ImportItem>(
      `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}/finalize`,
      { method: 'POST', signal: cb?.signal },
    )
    return item
  } catch (err) {
    // AbortError is user-initiated — bubble it through unchanged so
    // the queue layer can mark the file as 'cancelled'. For genuine
    // failures, wrap so callers know the upload id is still valid
    // server-side and a Retry button can resume.
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new UploadFailedError(uploadID, err as Error)
  }
}

// abortImportUpload tells the server to forget about an in-flight
// upload. Used when the user explicitly removes a file (vs. a
// transient failure that should leave the staging file in place).
export async function abortImportUpload(sessionId: string, uploadID: string): Promise<void> {
  const auth = await authHeader()
  await fetch(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}`,
    { method: 'DELETE', headers: auth ? { Authorization: auth } : {} },
  ).catch(() => { /* swallow — janitor reaps the upload after TTL */ })
}

// ChunkError is what uploadChunk rejects with on a non-aborted
// failure. Carries the HTTP status (or 0 for transport-level errors)
// so the retry logic can decide whether to back off or fail fast.
class ChunkError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ChunkError'
  }
}

// sendChunkWithRetry pushes one chunk, recovering from transient
// failures with exponential backoff and from 409 / lost-ACK races
// by re-syncing against the server's authoritative offset.
//
// Returns the new offset (== end on success) so the outer loop knows
// where the next chunk starts. If the server reports it already has
// MORE bytes than we expected (the lost-ACK case where our previous
// chunk landed but the response evaporated), we skip ahead to that
// offset and the outer loop's next iteration starts from there.
async function sendChunkWithRetry(
  sessionId: string,
  uploadID: string,
  file: File,
  start: number,
  end: number,
  cb?: UploadCallbacks,
): Promise<number> {
  let lastErr: Error | null = null
  for (let attempt = 0; attempt < MAX_CHUNK_ATTEMPTS; attempt++) {
    if (cb?.signal?.aborted) throw new DOMException('upload aborted', 'AbortError')
    try {
      await uploadChunk(sessionId, uploadID, file.slice(start, end), start, end - 1, file.size, cb)
      return end
    } catch (err) {
      // User-initiated aborts bubble straight up; we don't retry them.
      if (err instanceof DOMException && err.name === 'AbortError') throw err
      lastErr = err as Error

      const status = err instanceof ChunkError ? err.status : 0

      // 4xx (except 408/425/429) are logical errors — bad range, auth
      // failure, etc. — and won't fix themselves on retry.
      if (status >= 400 && status < 500 && status !== 408 && status !== 425 && status !== 429) {
        // 409: server has a different idea of where we are. GET status
        // and resume from there. This is the lost-ACK recovery path.
        if (status === 409) {
          const synced = await syncFromServerStatus(sessionId, uploadID, cb?.signal)
          if (synced > start) {
            // Server is ahead of us — skip the chunk we thought we
            // needed and let the outer loop start from `synced`.
            // Surface progress so the UI's transferred-bytes value
            // catches up to the server's reality.
            cb?.onProgress?.(file.size === 0 ? 100 : Math.round((synced / file.size) * 100), synced)
            return synced
          }
          // Server says it has fewer bytes than we already sent —
          // shouldn't happen, but if it does there's nothing to do
          // but treat as a hard failure.
        }
        throw err
      }

      // Network error or 5xx: back off and retry. See chunkBackoffMs
      // for the schedule (caps at 12 s on attempt 5).
      if (attempt < MAX_CHUNK_ATTEMPTS - 1) {
        await delay(chunkBackoffMs(attempt + 1), cb?.signal)
      }
    }
  }
  throw lastErr ?? new Error('chunk upload failed after retries')
}

// syncFromServerStatus asks the server how much of the upload it has
// already accepted, then returns that offset. Used when a 409 tells
// us our local offset is wrong.
async function syncFromServerStatus(
  sessionId: string,
  uploadID: string,
  signal?: AbortSignal,
): Promise<number> {
  const status = await jsonRequest<UploadStatusResponse>(
    `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}`,
    { signal },
  )
  return status.bytes_written
}

// delay resolves after ms, or rejects with AbortError if signal fires.
// Used between retry attempts so the caller can still cancel during
// the backoff window.
function delay(ms: number, signal?: AbortSignal): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('upload aborted', 'AbortError'))
      return
    }
    const t = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(t)
      reject(new DOMException('upload aborted', 'AbortError'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

// uploadChunk PUTs a single chunk via XHR so we can emit upload
// progress (fetch only reports response-side progress). Resolves on
// 2xx, rejects with AbortError on cancellation, rejects with
// ChunkError(status, message) on any other failure.
//
// XHR-level timeout bounds a stalled connection so we don't hang
// forever waiting for a half-dead TCP socket; the timeout fires
// onerror which sendChunkWithRetry treats as a transient failure.
async function uploadChunk(
  sessionId: string,
  uploadID: string,
  chunk: Blob,
  start: number,
  end: number,
  total: number,
  cb?: UploadCallbacks,
): Promise<void> {
  const auth = await authHeader()

  return new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(
      'PUT',
      `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}`,
    )
    xhr.timeout = CHUNK_TIMEOUT_MS
    if (auth) xhr.setRequestHeader('Authorization', auth)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.setRequestHeader('Content-Range', `bytes ${start}-${end}/${total}`)

    xhr.upload.onprogress = (ev) => {
      if (!cb?.onProgress || !ev.lengthComputable) return
      // Aggregate progress across chunks: previous offset (start) +
      // current chunk's loaded bytes.
      const transferred = start + ev.loaded
      const percent = total === 0 ? 100 : Math.round((transferred / total) * 100)
      cb.onProgress(percent, transferred)
    }

    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve()
      else {
        // Trim large response bodies — Cloudflare 5xx pages can be
        // multi-KB HTML that swamps any user-facing toast.
        const snippet = (xhr.responseText || '').slice(0, 160).trim()
        reject(new ChunkError(xhr.status, `chunk ${xhr.status}${snippet ? `: ${snippet}` : ''}`))
      }
    }
    xhr.onerror = () => reject(new ChunkError(0, 'network error — server unreachable or connection dropped'))
    xhr.ontimeout = () => reject(new ChunkError(0, `chunk timed out after ${CHUNK_TIMEOUT_MS / 1000}s`))
    xhr.onabort = () => reject(new DOMException('upload aborted', 'AbortError'))

    if (cb?.signal) {
      if (cb.signal.aborted) {
        xhr.abort()
        return
      }
      cb.signal.addEventListener('abort', () => xhr.abort(), { once: true })
    }

    xhr.send(chunk)
  })
}
