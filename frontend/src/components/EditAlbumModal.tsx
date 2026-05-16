import { useEffect, useRef, useState } from 'react'
import { updateAlbumTags, type AlbumTagsUpdate } from '../lib/api'
import type { Album } from '../lib/subsonic'

interface Props {
  album: Album
  onClose: () => void
  // onSaved fires after the PUT returns. The patch is forwarded so
  // the parent can record the user's intended new name — Navidrome's
  // hash-based album id shifts when name/artist change, so the parent
  // needs the new values to search for the renamed entry rather than
  // refetching a now-stale id.
  onSaved: (patch: AlbumTagsUpdate) => void
}

// FormState mirrors AlbumTagsUpdate but keeps everything as strings so
// empty inputs are first-class. The year field is a string until we
// diff and parse on submit.
interface FormState {
  album_artist: string
  album: string
  year: string
  genre: string
}

function albumToForm(album: Album): FormState {
  return {
    album_artist: '',
    album: album.name ?? '',
    year: album.year != null ? String(album.year) : '',
    genre: album.genre ?? '',
  }
}

function diffForm(initial: FormState, current: FormState): AlbumTagsUpdate {
  const patch: AlbumTagsUpdate = {}
  if (current.album_artist !== initial.album_artist) patch.album_artist = current.album_artist
  if (current.album !== initial.album) patch.album = current.album
  if (current.year !== initial.year) {
    const n = parseInt(current.year, 10)
    patch.year = Number.isFinite(n) && n > 0 ? n : 0
  }
  if (current.genre !== initial.genre) patch.genre = current.genre
  return patch
}

export default function EditAlbumModal({ album, onClose, onSaved }: Props) {
  const initialRef = useRef<FormState>(albumToForm(album))
  const [form, setForm] = useState<FormState>(initialRef.current)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const patch = diffForm(initialRef.current, form)
  const hasChanges = Object.keys(patch).length > 0

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!hasChanges || saving) return
    setSaving(true)
    setError('')
    try {
      const res = await updateAlbumTags(album.id, patch)
      if (res.failed_ids?.length > 0) {
        // Partial success is worth flagging — the user can decide
        // whether to retry. Don't close on partial failure.
        setError(`Updated ${res.updated_ids.length} of ${res.updated_ids.length + res.failed_ids.length} tracks. ${res.failed_ids.length} failed.`)
        setSaving(false)
        return
      }
      onSaved(patch)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed')
      setSaving(false)
    }
  }

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm(prev => ({ ...prev, [key]: value }))
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" role="dialog" aria-labelledby="edit-album-title" onClick={e => e.stopPropagation()}>
        <header className="modal-header">
          <h2 id="edit-album-title">Edit album</h2>
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
        </header>

        <div className="modal-warning" style={{ background: 'rgba(0, 186, 255, 0.08)', borderColor: 'rgba(0, 186, 255, 0.25)', color: 'var(--accent)' }}>
          Changes apply to every track in this album.
        </div>

        <form onSubmit={handleSubmit} className="modal-form">
          <label>
            <span>Album title</span>
            <input type="text" value={form.album} onChange={e => update('album', e.target.value)} disabled={saving} />
          </label>
          <label>
            <span>Album artist</span>
            <input
              type="text"
              value={form.album_artist}
              onChange={e => update('album_artist', e.target.value)}
              placeholder="(leave blank to keep)"
              disabled={saving}
            />
          </label>
          <label>
            <span>Year</span>
            <input type="number" min={0} value={form.year} onChange={e => update('year', e.target.value)} disabled={saving} />
          </label>
          <label>
            <span>Genre</span>
            <input type="text" value={form.genre} onChange={e => update('genre', e.target.value)} disabled={saving} />
          </label>

          {error && <div className="modal-error">{error}</div>}

          <footer className="modal-actions">
            <button type="button" className="btn-secondary" onClick={onClose} disabled={saving}>Cancel</button>
            <button type="submit" className="btn-primary" disabled={!hasChanges || saving}>
              {saving ? 'Saving…' : 'Save'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  )
}
