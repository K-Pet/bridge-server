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

export default function Import() {
  const { session, files, progress, busy, enqueue, removeFile, commit, abort } = useImport()
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
      const parts = [`${result.committed.length} file${result.committed.length === 1 ? '' : 's'} added`]
      if (result.skipped.length > 0) parts.push(`${result.skipped.length} skipped`)
      if (result.failed.length > 0) parts.push(`${result.failed.length} failed`)
      setNotice({
        kind: result.failed.length > 0 ? 'error' : 'success',
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
    <div className="library-page">
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
      <div
        className={`import-dropzone ${dragOver ? 'drag-over' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
        onDragLeave={() => setDragOver(false)}
        onDrop={handleDrop}
      >
        <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" opacity="0.5">
          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
          <polyline points="17 8 12 3 7 8" />
          <line x1="12" y1="3" x2="12" y2="15" />
        </svg>
        <p className="import-dropzone-title">Drop audio files or a folder here</p>
        <p className="import-dropzone-sub">
          Supported: MP3, FLAC, M4A, ALAC, AAC, OGG, OPUS, WAV, AIFF, WMA
        </p>
        <div className="import-picker-buttons">
          <button className="btn-primary" onClick={() => fileInputRef.current?.click()}>
            Choose files
          </button>
          <button className="btn-secondary" onClick={() => folderInputRef.current?.click()}>
            Choose folder
          </button>
        </div>
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

      {files.length > 0 && (
        <ImportProgressSummary
          totalFiles={progress.totalFiles}
          uploadedFiles={progress.uploadedFiles}
          totalBytes={progress.totalBytes}
          uploadedBytes={progress.uploadedBytes}
          inFlight={progress.inFlight}
          errorCount={errorCount}
        />
      )}

      {files.length > 0 && (
        <ImportFileGroups files={files} onRemove={removeFile} />
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
}) {
  const pct = props.totalBytes === 0 ? 0 : Math.round((props.uploadedBytes / props.totalBytes) * 100)
  return (
    <div className="import-progress-summary">
      <div className="import-progress-stats">
        <span>
          <strong>{props.uploadedFiles}/{props.totalFiles}</strong> files
          {props.inFlight > 0 && <> &middot; {props.inFlight} uploading</>}
          {props.errorCount > 0 && <> &middot; <span className="text-error">{props.errorCount} failed</span></>}
        </span>
        <span className="text-secondary">
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
//      came back) — shown in an "In transit" group at the top so the
//      user can watch progress without scrolling past every album.
//   2. Files with server-assigned plans — grouped into collapsible
//      cards by AlbumArtist → Album.
//
// Albums with any conflict or error default to expanded; everything
// else defaults to collapsed so a 50-album review isn't a wall of text.
//
// We compute groups inline (without memoisation across renders) on
// purpose: useMemo on a list-of-thousands key array doesn't help —
// the dep changes on every progress flush anyway.
function ImportFileGroups({
  files,
  onRemove,
}: {
  files: QueuedFile[]
  onRemove: (localId: string) => void
}) {
  // Files without a server item yet (still uploading or failed before
  // tags came back) go in a separate "in transit" list at the top.
  // Files with server-assigned plans key on Artist/Album from the
  // planner's effective tags (already sanitized + fallback-applied).
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
    const key = `${artist} ${album}`
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

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const toggle = (key: string) => {
    setCollapsed(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <div className="import-groups">
      {inTransit.length > 0 && (
        <ImportInTransitGroup files={inTransit} onRemove={onRemove} />
      )}
      {albumKeys.map(key => {
        const g = albumMap.get(key)!
        return (
          <ImportAlbumGroup
            key={key}
            artist={g.artist}
            album={g.album}
            files={g.files}
            collapsed={collapsed.has(key)}
            onToggle={() => toggle(key)}
            onRemove={onRemove}
          />
        )
      })}
    </div>
  )
}

function ImportInTransitGroup({
  files,
  onRemove,
}: {
  files: QueuedFile[]
  onRemove: (localId: string) => void
}) {
  const uploading = files.filter(f => f.status === 'uploading').length
  const pending = files.filter(f => f.status === 'pending').length
  return (
    <div className="import-group import-group-transit">
      <div className="import-group-header">
        <span className="import-group-title">In transit</span>
        <span className="import-group-meta">
          {uploading > 0 && <>{uploading} uploading</>}
          {uploading > 0 && pending > 0 && ' · '}
          {pending > 0 && <>{pending} queued</>}
        </span>
      </div>
      <div className="import-file-list">
        {files.map(f => (
          <ImportFileRow key={f.localId} file={f} onRemove={onRemove} />
        ))}
      </div>
    </div>
  )
}

function ImportAlbumGroup({
  artist, album, files, collapsed, onToggle, onRemove,
}: {
  artist: string
  album: string
  files: QueuedFile[]
  collapsed: boolean
  onToggle: () => void
  onRemove: (localId: string) => void
}) {
  const conflicts = files.filter(f => f.serverItem?.status === 'conflict').length
  const errors = files.filter(f => f.status === 'error' || f.status === 'cancelled').length
  const hasIssues = conflicts > 0 || errors > 0

  // Auto-expand albums with issues on first render. Once the user
  // explicitly toggles a key it sticks (via collapsed state above).
  // Without this, a 500-album import would hide every conflict behind
  // a click.
  const effectivelyCollapsed = hasIssues ? collapsed : !collapsed

  return (
    <div className={`import-group import-album-group ${hasIssues ? 'has-issues' : ''}`}>
      <button
        className="import-group-header import-group-toggle"
        onClick={onToggle}
        type="button"
        aria-expanded={!effectivelyCollapsed}
      >
        <svg
          className={`import-group-chevron ${effectivelyCollapsed ? '' : 'open'}`}
          width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        >
          <polyline points="9 18 15 12 9 6" />
        </svg>
        <div className="import-group-title-stack">
          <span className="import-group-title">{album}</span>
          <span className="import-group-subtitle">{artist}</span>
        </div>
        <span className="import-group-meta">
          {files.length} track{files.length === 1 ? '' : 's'}
          {conflicts > 0 && <> · <span className="text-warning">{conflicts} conflict{conflicts === 1 ? '' : 's'}</span></>}
          {errors > 0 && <> · <span className="text-error">{errors} failed</span></>}
        </span>
      </button>
      {!effectivelyCollapsed && (
        <div className="import-file-list">
          {files.map(f => (
            <ImportFileRow key={f.localId} file={f} onRemove={onRemove} />
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
}: {
  file: QueuedFile
  onRemove: (localId: string) => void
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
