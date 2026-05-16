import { useEffect, useRef, useState } from 'react'
import { updateSongTags, type SongTagsUpdate } from '../lib/api'
import type { Song } from '../lib/subsonic'

// Formats the server's tagwriter package can currently mutate. Keep
// in sync with internal/library/tagwriter/tagwriter.go::SupportsWrite.
const EDITABLE_SUFFIXES = new Set(['mp3', 'flac'])

interface Props {
  song: Song
  onClose: () => void
  // onSaved fires after the PUT returns; the server has triggered a
  // background Navidrome rescan, so the actual library view won't
  // reflect new tags until the scan completes and a library_updated
  // SSE event arrives. The parent usually relies on that event to
  // refresh.
  onSaved: (songId: string) => void
}

// FormState mirrors the SongTagsUpdate payload but uses strings for
// numeric fields so empty inputs are first-class (string vs. NaN is
// easier to reason about than a number that might be 0 or undefined).
interface FormState {
  title: string
  artist: string
  album_artist: string
  album: string
  year: string
  track_number: string
  disc_number: string
  genre: string
}

function songToForm(song: Song): FormState {
  return {
    title: song.title ?? '',
    artist: song.artist ?? '',
    album_artist: '',
    album: song.album ?? '',
    year: song.year != null ? String(song.year) : '',
    track_number: song.track != null ? String(song.track) : '',
    disc_number: song.discNumber != null ? String(song.discNumber) : '',
    genre: song.genre ?? '',
  }
}

// diffForm returns only the fields that changed from initial → current
// so the server gets a true patch (and we don't accidentally clobber a
// tag the user never touched).
function diffForm(initial: FormState, current: FormState): SongTagsUpdate {
  const patch: SongTagsUpdate = {}
  if (current.title !== initial.title) patch.title = current.title
  if (current.artist !== initial.artist) patch.artist = current.artist
  if (current.album_artist !== initial.album_artist) patch.album_artist = current.album_artist
  if (current.album !== initial.album) patch.album = current.album
  if (current.year !== initial.year) {
    const n = parseInt(current.year, 10)
    patch.year = Number.isFinite(n) && n > 0 ? n : 0
  }
  if (current.track_number !== initial.track_number) {
    const n = parseInt(current.track_number, 10)
    patch.track_number = Number.isFinite(n) && n > 0 ? n : 0
  }
  if (current.disc_number !== initial.disc_number) {
    const n = parseInt(current.disc_number, 10)
    patch.disc_number = Number.isFinite(n) && n > 0 ? n : 0
  }
  if (current.genre !== initial.genre) patch.genre = current.genre
  return patch
}

export default function EditSongModal({ song, onClose, onSaved }: Props) {
  const initialRef = useRef<FormState>(songToForm(song))
  const [form, setForm] = useState<FormState>(initialRef.current)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const suffix = (song.suffix ?? '').toLowerCase()
  const editable = EDITABLE_SUFFIXES.has(suffix)

  // Escape-to-close — wired to document so it works even when the
  // modal hasn't grabbed focus yet (e.g. user keyboards in from the
  // album page).
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
    if (!editable || !hasChanges || saving) return
    setSaving(true)
    setError('')
    try {
      await updateSongTags(song.id, patch)
      onSaved(song.id)
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
      <div className="modal" role="dialog" aria-labelledby="edit-song-title" onClick={e => e.stopPropagation()}>
        <header className="modal-header">
          <h2 id="edit-song-title">Edit metadata</h2>
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
        </header>

        {!editable && (
          <div className="modal-warning">
            Editing tags on <code>.{suffix || 'unknown'}</code> files isn't supported yet. Currently
            only <code>.mp3</code> and <code>.flac</code> can be edited.
          </div>
        )}

        <form onSubmit={handleSubmit} className="modal-form">
          <label>
            <span>Title</span>
            <input type="text" value={form.title} onChange={e => update('title', e.target.value)} disabled={!editable || saving} />
          </label>
          <label>
            <span>Artist</span>
            <input type="text" value={form.artist} onChange={e => update('artist', e.target.value)} disabled={!editable || saving} />
          </label>
          <label>
            <span>Album artist</span>
            <input
              type="text"
              value={form.album_artist}
              onChange={e => update('album_artist', e.target.value)}
              placeholder="(leave blank to keep)"
              disabled={!editable || saving}
            />
          </label>
          <label>
            <span>Album</span>
            <input type="text" value={form.album} onChange={e => update('album', e.target.value)} disabled={!editable || saving} />
          </label>
          <div className="modal-row">
            <label>
              <span>Year</span>
              <input type="number" min={0} value={form.year} onChange={e => update('year', e.target.value)} disabled={!editable || saving} />
            </label>
            <label>
              <span>Track #</span>
              <input type="number" min={0} value={form.track_number} onChange={e => update('track_number', e.target.value)} disabled={!editable || saving} />
            </label>
            <label>
              <span>Disc #</span>
              <input type="number" min={0} value={form.disc_number} onChange={e => update('disc_number', e.target.value)} disabled={!editable || saving} />
            </label>
          </div>
          <label>
            <span>Genre</span>
            <input type="text" value={form.genre} onChange={e => update('genre', e.target.value)} disabled={!editable || saving} />
          </label>

          {error && <div className="modal-error">{error}</div>}

          <footer className="modal-actions">
            <button type="button" className="btn-secondary" onClick={onClose} disabled={saving}>
              Cancel
            </button>
            <button type="submit" className="btn-primary" disabled={!editable || !hasChanges || saving}>
              {saving ? 'Saving…' : 'Save'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  )
}
