// ImportContext owns the in-flight library upload session for the
// signed-in user. It lives above <Routes/> so navigating between
// pages doesn't cancel uploads — the user can browse to /library,
// /settings, etc. while a 1000-file import runs in the background.
//
// Concurrency model: we keep a small pool (UPLOAD_CONCURRENCY) of
// uploads in flight at once. Higher numbers don't help — a single
// HTTP/1.1 connection-per-host limit, plus the bridge-server's
// per-file disk write, mean 4 parallel uploads is roughly the sweet
// spot for the LAN-style deployments this server targets. Beyond
// that you just queue at the connection layer.
//
// The queue is intentionally lossy on tab close: aborting an in-
// flight FormData upload mid-stream leaves a half-written file in
// the staging dir, which the server's session janitor reaps after
// the TTL. That's the right tradeoff vs. keeping queue state in
// localStorage and trying to resume across reloads (which would
// require chunked uploads with content-range, a bigger build).

import {
  createContext, useContext, useCallback, useEffect, useMemo,
  useRef, useState, type ReactNode,
} from 'react'
import {
  abortImportSession, commitImportSession, createImportSession,
  skipImportItem, uploadImportFile,
  type CommitResult, type ImportItem, type ImportSession,
} from '../lib/importApi'

const UPLOAD_CONCURRENCY = 4

// QueuedFile tracks a single user-selected file as it moves through
// the upload pipeline. State transitions (single-direction):
//
//   pending → uploading → uploaded
//                       → error
//                       → cancelled
type QueuedStatus = 'pending' | 'uploading' | 'uploaded' | 'error' | 'cancelled'

export interface QueuedFile {
  // localId is a client-only id the queue uses to identify a file
  // before it has a server-assigned ImportItem.id. Once uploaded,
  // serverItem is populated and the review UI keys off serverItem.id.
  localId: string
  file: File
  status: QueuedStatus
  progress: number       // 0-100
  bytesUploaded: number
  error?: string
  serverItem?: ImportItem
}

export interface ImportProgress {
  totalFiles: number
  uploadedFiles: number
  errorFiles: number
  inFlight: number
  totalBytes: number
  uploadedBytes: number
  // active is true whenever there's *anything* the user might want a
  // header indicator for — pending, uploading, or post-upload review.
  active: boolean
}

interface ImportContextValue {
  session: ImportSession | null
  files: QueuedFile[]
  progress: ImportProgress
  busy: boolean             // true while session is being created/committed/aborted
  enqueue(files: FileList | File[]): Promise<void>
  removeFile(localId: string): Promise<void>
  commit(allowOverwrite: boolean): Promise<CommitResult>
  abort(): Promise<void>
}

const ImportContext = createContext<ImportContextValue | null>(null)

export function useImport(): ImportContextValue {
  const ctx = useContext(ImportContext)
  if (!ctx) throw new Error('useImport must be used within ImportProvider')
  return ctx
}

export function ImportProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<ImportSession | null>(null)
  const [files, setFiles] = useState<QueuedFile[]>([])
  const [busy, setBusy] = useState(false)

  // Refs that the upload pump reads on every iteration so we don't have
  // to re-create the pump function whenever state changes (which would
  // race with in-flight uploads).
  const filesRef = useRef<QueuedFile[]>([])
  const sessionRef = useRef<ImportSession | null>(null)
  const inFlightRef = useRef(0)
  const abortControllers = useRef(new Map<string, AbortController>())
  // pumping guards against re-entrant pump invocations — only one
  // scheduling loop runs at a time, but it can dispatch up to
  // UPLOAD_CONCURRENCY uploads concurrently.
  const pumping = useRef(false)

  // progressBuffer batches XHR progress events across all in-flight
  // uploads. Without this, a 5000-file queue with 4 concurrent uploads
  // each firing ~10 progress events per second produces ~40 setStates/s,
  // each of which re-renders a 5000-row list. With the buffer the pump
  // flushes all pending progress into a single setState every 100ms.
  const progressBuffer = useRef(new Map<string, { progress: number; bytes: number }>())
  const flushTimer = useRef<number | null>(null)

  // writeFiles is the single entry point for queue state updates.
  // Updates filesRef synchronously *and* setFiles React state — the
  // pump reads the ref so it can see the latest queue without waiting
  // for a passive-effect flush. (Earlier versions synced the ref via
  // useEffect and dispatched the pump on setTimeout(0), which raced
  // because macrotasks fire before passive effects: the pump would
  // read a stale ref and skip the just-enqueued files.)
  const writeFiles = useCallback((updater: (prev: QueuedFile[]) => QueuedFile[]) => {
    const next = updater(filesRef.current)
    filesRef.current = next
    setFiles(next)
  }, [])

  // sessionRef is updated imperatively at the same time as setSession
  // (in ensureSession and resetLocalState). This effect is the safety
  // net that keeps it in sync if a future call site forgets the ref.
  useEffect(() => { sessionRef.current = session }, [session])

  // flushProgress applies every pending progress patch in one setState.
  // Cleared by terminal-state transitions (uploaded/error/cancelled),
  // which write directly via updateFile so the row's final state can't
  // be overwritten by a stale progress event.
  const flushProgress = useCallback(() => {
    flushTimer.current = null
    if (progressBuffer.current.size === 0) return
    const patches = progressBuffer.current
    progressBuffer.current = new Map()
    writeFiles((prev) => prev.map(f => {
      const p = patches.get(f.localId)
      if (!p || f.status !== 'uploading') return f
      return { ...f, progress: p.progress, bytesUploaded: p.bytes }
    }))
  }, [writeFiles])

  // queueProgress stages a progress update in the buffer and arms a
  // 250ms flush timer if one isn't already pending. Coalesces adjacent
  // events on the same file (only the latest progress per file is
  // kept). 250ms is the user-perception threshold for "live" progress
  // bars and gives the browser headroom to handle scroll/click events
  // when thousands of rows are visible.
  const queueProgress = useCallback((localId: string, progress: number, bytes: number) => {
    progressBuffer.current.set(localId, { progress, bytes })
    if (flushTimer.current == null) {
      flushTimer.current = window.setTimeout(flushProgress, 250)
    }
  }, [flushProgress])

  // updateFile applies a partial patch to one queued file by localId.
  // Used for state transitions (uploading/uploaded/error/cancelled);
  // progress events go through queueProgress instead. When a terminal
  // status arrives we drop any pending progress for that file so a
  // late flush doesn't clobber the final state.
  const updateFile = useCallback((localId: string, patch: Partial<QueuedFile>) => {
    progressBuffer.current.delete(localId)
    writeFiles((prev) => prev.map(f => f.localId === localId ? { ...f, ...patch } : f))
  }, [writeFiles])

  // pumpQueue fires off as many uploads as the concurrency budget
  // allows. Re-runs itself whenever an upload completes so the next
  // pending file slots in immediately.
  const pumpQueue = useCallback(async () => {
    if (pumping.current) return
    pumping.current = true
    try {
      // Loop until we either run out of pending files or we've saturated
      // the concurrency budget — whichever comes first. The setState
      // inside startUpload triggers a re-render, but we read from
      // filesRef so this loop sees the freshest list.
      while (inFlightRef.current < UPLOAD_CONCURRENCY) {
        const next = filesRef.current.find(f => f.status === 'pending')
        if (!next) break
        const sess = sessionRef.current
        if (!sess) break
        startUpload(next, sess.id)
      }
    } finally {
      pumping.current = false
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // startUpload kicks one file's upload and wires its lifecycle into
  // the queue state. Bumps inFlight on entry, decrements on exit, and
  // re-pumps the queue once the slot is free.
  const startUpload = useCallback((qf: QueuedFile, sessionId: string) => {
    const controller = new AbortController()
    abortControllers.current.set(qf.localId, controller)
    inFlightRef.current += 1
    updateFile(qf.localId, { status: 'uploading', progress: 0, bytesUploaded: 0 })

    uploadImportFile(sessionId, qf.file, {
      signal: controller.signal,
      onProgress: (percent, bytes) => {
        queueProgress(qf.localId, percent, bytes)
      },
    })
      .then((item) => {
        updateFile(qf.localId, {
          status: 'uploaded',
          progress: 100,
          bytesUploaded: qf.file.size,
          serverItem: item,
        })
      })
      .catch((err) => {
        if (err instanceof DOMException && err.name === 'AbortError') {
          updateFile(qf.localId, { status: 'cancelled', error: 'cancelled' })
        } else {
          updateFile(qf.localId, { status: 'error', error: (err as Error).message })
        }
      })
      .finally(() => {
        abortControllers.current.delete(qf.localId)
        inFlightRef.current -= 1
        // Drain the next slot. writeFiles updates filesRef synchronously,
        // so the pump can run inline without missing the just-completed
        // upload's status change.
        void pumpQueue()
      })
  }, [updateFile, queueProgress, pumpQueue])

  // ensureSession lazily creates the server-side session on the first
  // file the user adds. Avoids minting a session (and a staging dir)
  // until we know the user actually intends to import.
  const ensureSession = useCallback(async (): Promise<ImportSession> => {
    if (sessionRef.current) return sessionRef.current
    const created = await createImportSession()
    setSession(created)
    sessionRef.current = created
    return created
  }, [])

  const enqueue = useCallback(async (input: FileList | File[]) => {
    const arr = Array.from(input)
    if (arr.length === 0) return

    setBusy(true)
    try {
      await ensureSession()
    } finally {
      setBusy(false)
    }

    const queued: QueuedFile[] = arr.map((file) => ({
      localId: cryptoRandomId(),
      file,
      status: 'pending',
      progress: 0,
      bytesUploaded: 0,
    }))
    writeFiles((prev) => [...prev, ...queued])

    // writeFiles updated filesRef synchronously, so the pump can see
    // the new entries immediately — no setTimeout dance needed.
    void pumpQueue()
  }, [ensureSession, pumpQueue, writeFiles])

  const removeFile = useCallback(async (localId: string) => {
    const target = filesRef.current.find(f => f.localId === localId)
    if (!target) return

    // In-flight or pending: abort the upload and just drop the row.
    // Server never sees the file (or sees a half-written staging file
    // the janitor will reap with the session).
    const controller = abortControllers.current.get(localId)
    if (controller) controller.abort()

    // Already uploaded: tell the server to skip it on commit, then
    // drop the row from the local queue so the review screen forgets it.
    if (target.status === 'uploaded' && target.serverItem && sessionRef.current) {
      try {
        await skipImportItem(sessionRef.current.id, target.serverItem.id)
      } catch {
        // Skip is best-effort — if it fails the server will still
        // honor the commit's allow_overwrite=false default and the
        // user can re-skip. Don't block the UI on the error.
      }
    }

    writeFiles((prev) => prev.filter(f => f.localId !== localId))
  }, [writeFiles])

  // resetLocalState wipes the in-memory queue/session/timers. Shared
  // by commit and abort so a late-arriving progress flush after either
  // can't resurrect already-discarded rows.
  const resetLocalState = useCallback(() => {
    if (flushTimer.current != null) {
      clearTimeout(flushTimer.current)
      flushTimer.current = null
    }
    progressBuffer.current.clear()
    setSession(null)
    sessionRef.current = null
    setFiles([])
    filesRef.current = []
  }, [])

  const commit = useCallback(async (allowOverwrite: boolean): Promise<CommitResult> => {
    const sess = sessionRef.current
    if (!sess) throw new Error('no active import session')
    setBusy(true)
    try {
      const result = await commitImportSession(sess.id, allowOverwrite)
      resetLocalState()
      return result
    } finally {
      setBusy(false)
    }
  }, [resetLocalState])

  const abort = useCallback(async () => {
    const sess = sessionRef.current
    setBusy(true)
    try {
      // Cancel any in-flight uploads first so we don't race with the
      // server-side delete. Best-effort — abort() is async and we don't
      // wait for the network calls to bail out.
      for (const ctrl of abortControllers.current.values()) ctrl.abort()
      abortControllers.current.clear()
      if (sess) {
        try { await abortImportSession(sess.id) } catch { /* swallow */ }
      }
      resetLocalState()
    } finally {
      setBusy(false)
    }
  }, [resetLocalState])

  const progress: ImportProgress = useMemo(() => {
    let totalBytes = 0
    let uploadedBytes = 0
    let uploaded = 0
    let errored = 0
    let inFlight = 0
    for (const f of files) {
      totalBytes += f.file.size
      uploadedBytes += f.bytesUploaded
      if (f.status === 'uploaded') uploaded++
      else if (f.status === 'error' || f.status === 'cancelled') errored++
      else if (f.status === 'uploading') inFlight++
    }
    return {
      totalFiles: files.length,
      uploadedFiles: uploaded,
      errorFiles: errored,
      inFlight,
      totalBytes,
      uploadedBytes,
      active: files.length > 0 || session !== null,
    }
  }, [files, session])

  // Warn the user before they reload the tab while uploads are in flight.
  // Closing kills the XHRs mid-stream — losing whatever's in flight —
  // even though already-staged uploads survive on the server.
  useEffect(() => {
    function handler(e: BeforeUnloadEvent) {
      if (progress.inFlight > 0 || files.some(f => f.status === 'pending')) {
        // preventDefault triggers the browser's native "leave site?"
        // prompt. The legacy returnValue assignment is still required
        // by Safari/Firefox to actually show the dialog, even though
        // TS marks it deprecated for direct use.
        e.preventDefault()
        ;(e as BeforeUnloadEvent & { returnValue: string }).returnValue = ''
      }
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [progress.inFlight, files])

  const value: ImportContextValue = {
    session,
    files,
    progress,
    busy,
    enqueue,
    removeFile,
    commit,
    abort,
  }
  return <ImportContext.Provider value={value}>{children}</ImportContext.Provider>
}

// cryptoRandomId returns a short opaque id usable as a React key.
// Doesn't need cryptographic strength — these never leave the client —
// but crypto.randomUUID() is the simplest collision-free generator
// available in evergreen browsers.
function cryptoRandomId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID()
  }
  return Math.random().toString(36).slice(2) + Date.now().toString(36)
}
