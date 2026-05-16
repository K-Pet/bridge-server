import { useEffect, useState } from 'react'
import { renameArtist, type RenameArtistAck } from '../lib/api'
import type { Artist } from '../lib/subsonic'

interface Props {
  artist: Artist
  onClose: () => void
  // onSaved fires when the server has *accepted* the rename. The
  // actual cascade arrives later via SSE — the parent shows a
  // "renaming…" toast immediately and replaces it with the cascade
  // summary on completion.
  onSaved: (newName: string, ack: RenameArtistAck) => void
}

export default function EditArtistModal({ artist, onClose, onSaved }: Props) {
  const [name, setName] = useState(artist.name ?? '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const trimmed = name.trim()
  const changed = trimmed.length > 0 && trimmed !== artist.name.trim()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!changed || saving) return
    setSaving(true)
    setError('')
    try {
      const ack = await renameArtist(artist.id, trimmed)
      onSaved(trimmed, ack)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed')
      setSaving(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" role="dialog" aria-labelledby="rename-artist-title" onClick={e => e.stopPropagation()}>
        <header className="modal-header">
          <h2 id="rename-artist-title">Rename artist</h2>
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
        </header>

        <div className="modal-warning" style={{ background: 'rgba(0, 186, 255, 0.08)', borderColor: 'rgba(0, 186, 255, 0.25)', color: 'var(--accent)' }}>
          Renames every album by this artist and every solo track. Tracks credited with features
          (e.g. "Artist feat. Other") are left as-is so the credit stays intact.
        </div>

        <form onSubmit={handleSubmit} className="modal-form">
          <label>
            <span>Artist name</span>
            <input
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              disabled={saving}
              autoFocus
            />
          </label>

          {error && <div className="modal-error">{error}</div>}

          <footer className="modal-actions">
            <button type="button" className="btn-secondary" onClick={onClose} disabled={saving}>Cancel</button>
            <button type="submit" className="btn-primary" disabled={!changed || saving}>
              {saving ? 'Saving…' : 'Rename'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  )
}
