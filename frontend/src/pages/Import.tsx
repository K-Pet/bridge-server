// Import page: select files (or a whole folder) → upload concurrently →
// review the planned layout → commit. Navigating away keeps uploads
// running because the queue lives in ImportContext, which mounts above
// <Routes/>.

import { memo, useMemo, useRef, useState, type ChangeEvent, type DragEvent } from 'react'
import { useImport, type QueuedFile } from '../context/ImportContext'
import { type ImportItem } from '../lib/importApi'
import { readDataTransferFiles } from '../lib/dropFiles'

// SUPPORTED_EXTENSIONS mirrors the server's allowlist. Used to filter
// folder-pick uploads down to audio (folders typically contain cover
// art, log files, .DS_Store, etc. that we'd otherwise spend round-trips
// rejecting one-by-one).
const SUPPORTED_EXTENSIONS = new Set([
  '.mp3', '.flac', '.m4a', '.aac', '.ogg', '.oga',
  '.opus', '.wav', '.aiff', '.aif', '.wma', '.alac',
])

function fileExt(name: string): string {
  const idx = name.lastIndexOf('.')
  return idx === -1 ? '' : name.slice(idx).toLowerCase()
}

function filterAudio(files: File[]): { audio: File[]; rejected: number } {
  const audio: File[] = []
  let rejected = 0
  for (const f of files) {
    if (SUPPORTED_EXTENSIONS.has(fileExt(f.name))) audio.push(f)
    else rejected++
  }
  return { audio, rejected }
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GiB`
}

// gradientFor returns a deterministic two-stop gradient for an album
// cover tile based on the artist+album key. Same input always returns
// the same gradient so re-renders during upload don't flicker the tile,
// and adjacent albums in the list look visually distinct.
function gradientFor(key: string): string {
  let hash = 0
  for (let i = 0; i < key.length; i++) {
    hash = (hash * 31 + key.charCodeAt(i)) | 0
  }
  const h1 = Math.abs(hash) % 360
  const h2 = (h1 + 47) % 360
  return `linear-gradient(135deg, hsl(${h1} 55% 38%), hsl(${h2} 55% 22%))`
}

export default function Import() {
  const { session, files, progress, busy, enqueue, removeFile, retry, retryAll, commit, abort } = useImport()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const folderInputRef = useRef<HTMLInputElement>(null)
  const [notice, setNotice] = useState<{ kind: 'info' | 'error' | 'success'; text: string } | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [committing, setCommitting] = useState(false)

  // Files in conflict need explicit user action — either skip them
  // (default) or commit with allow_overwrite=true. Surface the
  // distinction so the commit button reflects what's about to happen.
  const conflictCount = useMemo(
    () => files.filter(f => f.serverItem?.status === 'conflict').length,
    [files],
  )
  const errorCount = useMemo(
    () => files.filter(f => f.status === 'error' || f.status === 'cancelled').length,
    [files],
  )
  const readyCount = useMemo(
    () => files.filter(f => f.status === 'uploaded' && f.serverItem?.status === 'staged').length,
    [files],
  )
  const allDone = files.length > 0 && progress.inFlight === 0
    && files.every(f => f.status === 'uploaded' || f.status === 'error' || f.status === 'cancelled')
  const hasFiles = files.length > 0

  function handleFilesPicked(input: FileList | File[] | null) {
    if (!input) return
    const arr = Array.isArray(input) ? input : Array.from(input)
    const { audio, rejected } = filterAudio(arr)
    if (rejected > 0) {
      setNotice({
        kind: 'info',
        text: `Skipped ${rejected} non-audio file${rejected === 1 ? '' : 's'}.`,
      })
    }
    if (audio.length === 0) return
    enqueue(audio).catch((err) => {
      setNotice({ kind: 'error', text: err instanceof Error ? err.message : String(err) })
    })
  }

  // handleDrop walks DataTransferItem entries so dropping a folder
  // (potentially with nested subfolders, e.g. ~/Music/Artist/Album/)
  // recursively yields every file inside. dataTransfer.files alone
  // would silently drop everything below the top level.
  async function handleDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault()
    setDragOver(false)
    try {
      const files = await readDataTransferFiles(e.dataTransfer)
      handleFilesPicked(files)
    } catch (err) {
      setNotice({ kind: 'error', text: err instanceof Error ? err.message : String(err) })
    }
  }

  async function handleCommit(allowOverwrite: boolean) {
    setCommitting(true)
    setNotice(null)
    try {
      const result = await commit(allowOverwrite)
      // Older server builds return null instead of [] when a category
      // is empty (Go encodes nil slices as null). Normalize so the UI
      // doesn't crash on `.length` against an unexpected null.
      const committed = result.committed ?? []
      const skipped = result.skipped ?? []
      const failed = result.failed ?? []
      const parts = [`${committed.length} file${committed.length === 1 ? '' : 's'} added`]
      if (skipped.length > 0) parts.push(`${skipped.length} skipped`)
      if (failed.length > 0) parts.push(`${failed.length} failed`)
      setNotice({
        kind: failed.length > 0 ? 'error' : 'success',
        text: `Import complete — ${parts.join(', ')}. Library is rescanning.`,
      })
    } catch (err) {
      setNotice({ kind: 'error', text: err instanceof Error ? err.message : String(err) })
    } finally {
      setCommitting(false)
    }
  }

  async function handleAbort() {
    if (!confirm('Discard this import? All pending and uploaded files will be removed from the staging area.')) return
    setNotice(null)
    try {
      await abort()
      setNotice({ kind: 'info', text: 'Import discarded.' })
    } catch (err) {
      setNotice({ kind: 'error', text: err instanceof Error ? err.message : String(err) })
    }
  }

  return (
    <div className="library-page import-page">
      <div className="library-header">
        <h2>Import Music</h2>
        {session && (
          <button
            className="btn-secondary"
            onClick={handleAbort}
            disabled={busy}
            title="Cancel this import and discard staged files"
          >
            Discard
          </button>
        )}
      </div>

      {notice && (
        <div className={`marketplace-toast toast-${notice.kind === 'error' ? 'warning' : notice.kind}`}>
          {notice.text}
          <button className="toast-close" onClick={() => setNotice(null)}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12" /></svg>
          </button>
        </div>
      )}

      {/* ── Picker / dropzone ──────────────────────────────────── */}
      {/* Compact mode once files exist: a slim "add more" strip so the
          dropzone doesn't dominate the screen during review. Click or
          drag onto it to add more files. */}
      <div
        className={`import-dropzone ${dragOver ? 'drag-over' : ''} ${hasFiles ? 'compact' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
        onDragLeave={() => setDragOver(false)}
        onDrop={handleDrop}
      >
        {hasFiles ? (
          <div className="import-dropzone-compact">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" opacity="0.7">
              <path d="M12 5v14M5 12h14" />
            </svg>
            <span>Drop more audio here, or</span>
            <button className="link-button" onClick={() => fileInputRef.current?.click()}>choose files</button>
            <span className="text-tertiary">·</span>
            <button className="link-button" onClick={() => folderInputRef.current?.click()}>choose folder</button>
          </div>
        ) : (
          <>
            <div className="import-dropzone-icon" aria-hidden>
              <svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
                <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                <polyline points="17 8 12 3 7 8" />
                <line x1="12" y1="3" x2="12" y2="15" />
              </svg>
            </div>
            <p className="import-dropzone-title">Drop audio files or a folder here</p>
            <p className="import-dropzone-sub">
              Supported · MP3 · FLAC · M4A · ALAC · AAC · OGG · OPUS · WAV · AIFF · WMA
            </p>
            <div className="import-picker-buttons">
              <button className="btn-primary" onClick={() => fileInputRef.current?.click()}>
                Choose files
              </button>
              <button className="btn-secondary" onClick={() => folderInputRef.current?.click()}>
                Choose folder
              </button>
            </div>
          </>
        )}
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept={Array.from(SUPPORTED_EXTENSIONS).join(',')}
          style={{ display: 'none' }}
          onChange={(e: ChangeEvent<HTMLInputElement>) => {
            handleFilesPicked(e.target.files)
            e.target.value = '' // allow re-picking the same files
          }}
        />
        <input
          ref={folderInputRef}
          type="file"
          // webkitdirectory is the only cross-browser way to pick a
          // whole directory tree from a desktop browser. Chrome/Edge/Safari
          // honor it; Firefox added support in 2018. The TS DOM types still
          // don't include it as of TS 5.6, so we cast.
          {...({ webkitdirectory: '' } as Record<string, string>)}
          multiple
          style={{ display: 'none' }}
          onChange={(e: ChangeEvent<HTMLInputElement>) => {
            handleFilesPicked(e.target.files)
            e.target.value = ''
          }}
        />
      </div>

      {hasFiles && (
        <ImportProgressSummary
          totalFiles={progress.totalFiles}
          uploadedFiles={progress.uploadedFiles}
          totalBytes={progress.totalBytes}
          uploadedBytes={progress.uploadedBytes}
          inFlight={progress.inFlight}
          errorCount={errorCount}
          allDone={allDone}
        />
      )}

      {hasFiles && (
        <ImportFileGroups
          files={files}
          onRemove={removeFile}
          onRetry={retry}
          onRetryAll={retryAll}
          errorCount={errorCount}
          allDone={allDone}
        />
      )}

      {allDone && (
        <div className="import-commit-bar">
          <div className="import-commit-summary">
            <strong>{readyCount}</strong> ready to add
            {conflictCount > 0 && (
              <>, <strong className="text-warning">{conflictCount}</strong> conflict{conflictCount === 1 ? '' : 's'}</>
            )}
            {errorCount > 0 && (
              <>, <strong className="text-error">{errorCount}</strong> failed</>
            )}
          </div>
          <div className="import-commit-actions">
            {conflictCount > 0 && (
              <button
                className="btn-secondary"
                onClick={() => handleCommit(true)}
                disabled={committing || busy}
                title="Replace existing files with the imported versions"
              >
                {committing ? 'Importing...' : `Add ${readyCount + conflictCount} (overwrite)`}
              </button>
            )}
            <button
              className="btn-primary"
              onClick={() => handleCommit(false)}
              disabled={committing || busy || readyCount === 0}
            >
              {committing ? 'Importing...' : `Add ${readyCount} to library`}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Sub-components ────────────────────────────────────────────────────

function ImportProgressSummary(props: {
  totalFiles: number
  uploadedFiles: number
  totalBytes: number
  uploadedBytes: number
  inFlight: number
  errorCount: number
  allDone: boolean
}) {
  const pct = props.totalBytes === 0 ? 0 : Math.round((props.uploadedBytes / props.totalBytes) * 100)
  const remaining = props.totalFiles - props.uploadedFiles - props.errorCount
  return (
    <div className={`import-progress-summary ${props.allDone ? 'is-done' : ''}`}>
      <div className="import-progress-stats">
        <div className="import-progress-headline">
          {props.allDone ? (
            <>
              <strong>{props.uploadedFiles}</strong>
              <span className="text-secondary">of {props.totalFiles} uploaded — review and confirm</span>
            </>
          ) : (
            <>
              <strong>{props.uploadedFiles}</strong>
              <span className="text-secondary">/ {props.totalFiles} uploaded</span>
              {props.inFlight > 0 && <span className="import-progress-chip">{props.inFlight} uploading</span>}
              {remaining - props.inFlight > 0 && (
                <span className="import-progress-chip subtle">{remaining - props.inFlight} queued</span>
              )}
              {props.errorCount > 0 && (
                <span className="import-progress-chip error">{props.errorCount} failed</span>
              )}
            </>
          )}
        </div>
        <span className="text-secondary import-progress-bytes">
          {formatBytes(props.uploadedBytes)} / {formatBytes(props.totalBytes)}
        </span>
      </div>
      <div className="import-progress-bar">
        <div className="import-progress-fill" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

// ImportFileGroups splits the queue into:
//   1. Files still on the wire (pending/uploading/error before tags
//      came back) — shown in their own bounded "in transit" panel so
//      live progress flushes don't push the album review off-screen.
//   2. Files with server-assigned plans — grouped into collapsible
//      cards by AlbumArtist → Album.
//
// Default expansion follows the upload phase: collapsed while uploads
// run (focus on progress), expanded once allDone (focus on review).
// Albums with conflicts/errors are always expanded so issues can't hide.
// User toggles override the default per album.
function ImportFileGroups({
  files,
  onRemove,
  onRetry,
  onRetryAll,
  errorCount,
  allDone,
}: {
  files: QueuedFile[]
  onRemove: (localId: string) => void
  onRetry: (localId: string) => void
  onRetryAll: () => void
  errorCount: number
  allDone: boolean
}) {
  const inTransit: QueuedFile[] = []
  const albumKeys: string[] = []
  const albumMap = new Map<string, { artist: string; album: string; files: QueuedFile[] }>()

  for (const f of files) {
    const item = f.serverItem
    if (!item) {
      inTransit.push(f)
      continue
    }
    const artist = item.plan.Effective.AlbumArtist || item.plan.Effective.Artist || 'Unknown Artist'
    const album = item.plan.Effective.Album || 'Unknown Album'
    const key = `${artist} ${album}`
    let bucket = albumMap.get(key)
    if (!bucket) {
      bucket = { artist, album, files: [] }
      albumMap.set(key, bucket)
      albumKeys.push(key)
    }
    bucket.files.push(f)
  }

  // Sort keys so the review screen is stable: alphabetical by artist,
  // then album. Otherwise the order would shift around as uploads
  // complete in arbitrary order.
  albumKeys.sort((a, b) => a.localeCompare(b))

  // userOverrides stores explicit per-album toggles. Falls back to the
  // phase default (allDone) when an album hasn't been toggled yet.
  const [userOverrides, setUserOverrides] = useState<Map<string, boolean>>(new Map())
  const setExpanded = (key: string, expanded: boolean) => {
    setUserOverrides(prev => {
      const next = new Map(prev)
      next.set(key, expanded)
      return next
    })
  }
  const expandAll = () => {
    const m = new Map<string, boolean>()
    for (const k of albumKeys) m.set(k, true)
    setUserOverrides(m)
  }
  const collapseAll = () => {
    const m = new Map<string, boolean>()
    for (const k of albumKeys) m.set(k, false)
    setUserOverrides(m)
  }

  const totalTracks = albumKeys.reduce((acc, k) => acc + (albumMap.get(k)?.files.length ?? 0), 0)

  return (
    <div className="import-groups">
      {inTransit.length > 0 && (
        <ImportInTransitGroup
          files={inTransit}
          onRemove={onRemove}
          onRetry={onRetry}
          onRetryAll={onRetryAll}
          errorCount={errorCount}
        />
      )}

      {albumKeys.length > 0 && (
        <div className="import-review-section">
          <div className="import-review-header">
            <span className="import-review-title">
              {albumKeys.length} album{albumKeys.length === 1 ? '' : 's'}
              <span className="text-tertiary"> · {totalTracks} track{totalTracks === 1 ? '' : 's'}</span>
            </span>
            {albumKeys.length > 1 && (
              <div className="import-review-actions">
                <button className="link-button" onClick={expandAll}>Expand all</button>
                <span className="text-tertiary">·</span>
                <button className="link-button" onClick={collapseAll}>Collapse all</button>
              </div>
            )}
          </div>

          <div className="import-album-list">
            {albumKeys.map(key => {
              const g = albumMap.get(key)!
              const conflicts = g.files.filter(f => f.serverItem?.status === 'conflict').length
              const errors = g.files.filter(f => f.status === 'error' || f.status === 'cancelled').length
              const hasIssues = conflicts > 0 || errors > 0
              const override = userOverrides.get(key)
              // Issues always expanded unless user explicitly collapsed them.
              const expanded = override !== undefined ? override : (hasIssues || allDone)
              return (
                <ImportAlbumGroup
                  key={key}
                  groupKey={key}
                  artist={g.artist}
                  album={g.album}
                  files={g.files}
                  conflicts={conflicts}
                  errors={errors}
                  expanded={expanded}
                  onToggle={() => setExpanded(key, !expanded)}
                  onRemove={onRemove}
                  onRetry={onRetry}
                />
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

function ImportInTransitGroup({
  files,
  onRemove,
  onRetry,
  onRetryAll,
  errorCount,
}: {
  files: QueuedFile[]
  onRemove: (localId: string) => void
  onRetry: (localId: string) => void
  onRetryAll: () => void
  errorCount: number
}) {
  const uploading = files.filter(f => f.status === 'uploading').length
  const pending = files.filter(f => f.status === 'pending').length
  return (
    <div className="import-group import-group-transit">
      <div className="import-group-header">
        <span className="import-transit-pulse" aria-hidden />
        <span className="import-group-title">In transit</span>
        <span className="import-group-meta">
          {uploading > 0 && <>{uploading} uploading</>}
          {uploading > 0 && pending > 0 && ' · '}
          {pending > 0 && <>{pending} queued</>}
        </span>
        {/* "Retry failed" lives here so the user can rescue multiple
            stalls in one click. Only appears when there's something
            to retry — keeps the chrome quiet on the happy path. */}
        {errorCount > 0 && (
          <button
            type="button"
            className="link-button import-retry-all"
            onClick={onRetryAll}
            title="Retry every failed upload"
          >
            Retry {errorCount} failed
          </button>
        )}
      </div>
      {/* This is the only inner-scroll container that survives the
          redesign — live progress churn here would otherwise push album
          review off-screen as files arrive. */}
      <div className="import-file-list import-file-list-scroll">
        {files.map(f => (
          <ImportFileRow key={f.localId} file={f} onRemove={onRemove} onRetry={onRetry} />
        ))}
      </div>
    </div>
  )
}

function ImportAlbumGroup({
  groupKey, artist, album, files, conflicts, errors, expanded, onToggle, onRemove, onRetry,
}: {
  groupKey: string
  artist: string
  album: string
  files: QueuedFile[]
  conflicts: number
  errors: number
  expanded: boolean
  onToggle: () => void
  onRemove: (localId: string) => void
  onRetry: (localId: string) => void
}) {
  const hasIssues = conflicts > 0 || errors > 0

  return (
    <div className={`import-group import-album-group ${hasIssues ? 'has-issues' : ''}`}>
      <button
        className="import-album-header"
        onClick={onToggle}
        type="button"
        aria-expanded={expanded}
      >
        <div
          className="import-album-cover"
          style={{ background: gradientFor(groupKey) }}
          aria-hidden
        >
          <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" opacity="0.85">
            <circle cx="12" cy="12" r="9" />
            <circle cx="12" cy="12" r="3" />
          </svg>
        </div>
        <div className="import-album-info">
          <div className="import-album-title" title={album}>{album}</div>
          <div className="import-album-artist" title={artist}>{artist}</div>
        </div>
        <div className="import-album-pills">
          {conflicts > 0 && (
            <span className="import-pill import-pill-warning">
              {conflicts} conflict{conflicts === 1 ? '' : 's'}
            </span>
          )}
          {errors > 0 && (
            <span className="import-pill import-pill-error">
              {errors} failed
            </span>
          )}
          <span className="import-pill">
            {files.length} track{files.length === 1 ? '' : 's'}
          </span>
        </div>
        <svg
          className={`import-album-chevron ${expanded ? 'open' : ''}`}
          width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
          aria-hidden
        >
          <polyline points="9 18 15 12 9 6" />
        </svg>
      </button>
      {expanded && (
        <div className="import-album-tracks">
          {files.map(f => (
            <ImportFileRow key={f.localId} file={f} onRemove={onRemove} onRetry={onRetry} />
          ))}
        </div>
      )}
    </div>
  )
}

// ImportFileRow is wrapped in React.memo because it dominates render
// cost during active uploads — a 1000-file import re-renders this
// row every progress flush (every 250ms) for the rows that actually
// changed, and we don't want the unchanged rows in that list to do
// any work. The queue's writeFiles updater preserves object identity
// for files whose state didn't change, so memo's default Object.is
// prop-comparison is enough; we just need a stable onRemove (which
// is now `(localId) => void` so the parent can pass the same
// callback reference to every row).
const ImportFileRow = memo(function ImportFileRow({
  file,
  onRemove,
  onRetry,
}: {
  file: QueuedFile
  onRemove: (localId: string) => void
  onRetry: (localId: string) => void
}) {
  const item = file.serverItem
  // Inside an album group the artist/album are already in the header,
  // so we only show the planned filename portion. Full RelPath is the
  // tooltip for users who want to see exactly where the file lands.
  const planRel = item?.plan.RelPath
  const planFilename = planRel ? planRel.split('/').pop() : null
  const flags = useMemo(() => {
    if (!item) return [] as string[]
    const out: string[] = []
    if (item.plan.MissingArtist) out.push('no artist')
    if (item.plan.MissingAlbum) out.push('no album')
    if (item.plan.MissingTitle) out.push('no title')
    return out
  }, [item])
  const canRetry = file.status === 'error' || file.status === 'cancelled'

  return (
    <div className={`import-file-row import-file-${file.status}`}>
      <div className="import-file-status">
        <StatusIcon file={file} item={item} />
      </div>
      <div className="import-file-body">
        <div className="import-file-name" title={file.file.name}>{file.file.name}</div>
        <div className="import-file-meta">
          {planFilename ? (
            <span className="import-file-dest" title={planRel || ''}>→ {planFilename}</span>
          ) : (
            <span className="text-secondary">{formatBytes(file.file.size)}</span>
          )}
          {flags.length > 0 && (
            <span className="import-file-flags"> ({flags.join(', ')})</span>
          )}
          {file.error && <span className="text-error"> — {file.error}</span>}
        </div>
        {file.status === 'uploading' && (
          <div className="import-file-progress">
            <div className="import-file-progress-fill" style={{ width: `${file.progress}%` }} />
          </div>
        )}
      </div>
      <div className="import-file-actions">
        {canRetry && (
          <button
            type="button"
            className="btn-icon import-retry-btn"
            onClick={() => onRetry(file.localId)}
            title={file.uploadID ? 'Resume upload from where it stopped' : 'Retry upload'}
            aria-label={`Retry ${file.file.name}`}
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <polyline points="1 4 1 10 7 10" />
              <path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10" />
            </svg>
          </button>
        )}
        <button
          className="btn-icon"
          onClick={() => onRemove(file.localId)}
          title="Remove from import"
          aria-label={`Remove ${file.file.name}`}
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M18 6L6 18M6 6l12 12" />
          </svg>
        </button>
      </div>
    </div>
  )
})

function StatusIcon({ file, item }: { file: QueuedFile; item?: ImportItem }) {
  // Server-side conflict status takes priority over client status —
  // a successfully uploaded file is still "not OK" if its destination
  // collides with something already on disk.
  if (item?.status === 'conflict') {
    return <span className="status-dot status-warning" title="Destination already exists" />
  }
  switch (file.status) {
    case 'pending':
      return <span className="status-dot status-pending" title="Waiting to upload" />
    case 'uploading':
      return <span className="spinner-sm" title="Uploading" />
    case 'uploaded':
      return <span className="status-dot status-success" title="Uploaded" />
    case 'error':
      return <span className="status-dot status-error" title="Failed" />
    case 'cancelled':
      return <span className="status-dot status-error" title="Cancelled" />
  }
}
