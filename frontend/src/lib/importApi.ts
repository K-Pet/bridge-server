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
}

interface BeginUploadResponse {
  upload_id: string
  chunk_size: number
}

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

  const uploadID = begin.upload_id
  // Don't follow the server's hint past 16 MiB — even servers tolerant
  // of larger chunks risk hitting an upstream proxy cap we can't see.
  const chunkSize = Math.min(begin.chunk_size, 16 * 1024 * 1024)

  try {
    let offset = 0
    while (offset < file.size) {
      const end = Math.min(offset + chunkSize, file.size)
      await uploadChunk(sessionId, uploadID, file.slice(offset, end), offset, end - 1, file.size, cb)
      offset = end
      cb?.onProgress?.(file.size === 0 ? 100 : Math.round((offset / file.size) * 100), offset)
    }

    const item = await jsonRequest<ImportItem>(
      `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}/finalize`,
      { method: 'POST', signal: cb?.signal },
    )
    return item
  } catch (err) {
    // Tell the server to drop the staged bytes on any failure.
    // Best-effort; the session janitor reaps it eventually if this
    // network call also fails.
    fetch(
      `/api/library/import/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadID)}`,
      { method: 'DELETE', headers: (await authHeader()) ? { Authorization: (await authHeader())! } : {} },
    ).catch(() => { /* swallow */ })
    throw err
  }
}

// uploadChunk PUTs a single chunk via XHR so we can emit upload
// progress (fetch only reports response-side progress). Resolves on
// 2xx, rejects with AbortError on cancellation, otherwise rejects
// with a descriptive error message.
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
      else reject(new Error(`chunk failed: ${xhr.status} ${xhr.responseText}`))
    }
    xhr.onerror = () => reject(new Error('chunk network error'))
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
