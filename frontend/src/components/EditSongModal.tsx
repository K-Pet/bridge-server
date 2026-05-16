import { useEffect, useRef, useState } from 'react'
import { identifySong, updateSongTags, type IdentifyCandidate, type SongTagsUpdate } from '../lib/api'
import { getConfig } from '../lib/supabase'
import type { Song } from '../lib/subsonic'

// Formats the server's tagwriter package can currently mutate. Keep
// in sync with internal/library/tagwriter/tagwriter.go::SupportsWrite.
// MP3/FLAC use Go-native writers; the rest go through ffmpeg -c copy.
const EDITABLE_SUFFIXES = new Set(['mp3', 'flac', 'ogg', 'oga', 'opus', 'm4a', 'aac', 'alac'])

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
//
// Song-scope edits never include `album` or `album_artist` — those are
// album-wide attributes. Letting a single song change them would split
// it off into a phantom album under a different artist. Album-level
// fields are exposed only by EditAlbumModal.
interface FormState {
  title: string
  artist: string
  year: string
  track_number: string
  disc_number: string
  genre: string
}

function songToForm(song: Song): FormState {
  return {
    title: song.title ?? '',
    artist: song.artist ?? '',
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
  const [identifying, setIdentifying] = useState(false)
  const [candidates, setCandidates] = useState<IdentifyCandidate[] | null>(null)

  const suffix = (song.suffix ?? '').toLowerCase()
  const editable = EDITABLE_SUFFIXES.has(suffix)
  // getConfig throws if called before initConfig — guard so the modal
  // doesn't crash if it somehow mounts before the app's config-fetch
  // settles (e.g. during a hot-reload).
  let acoustidAvailable = false
  try {
    acoustidAvailable = !!getConfig().acoustid_available
  } catch {
    acoustidAvailable = false
  }

  async function handleIdentify() {
    setIdentifying(true)
    setError('')
    setCandidates(null)
    try {
      const res = await identifySong(song.id)
      setCandidates(res.candidates)
      if (res.candidates.length === 0) {
        setError('No MusicBrainz matches found for this track.')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Identify failed')
    } finally {
      setIdentifying(false)
    }
  }

  function applyCandidate(c: IdentifyCandidate) {
    // Fill the form with the candidate's values. The user can still
    // tweak before saving — we don't auto-submit, since AcoustID can
    // return matches at low confidence that aren't actually right.
    //
    // disc_number is only auto-applied for true multi-disc releases
    // (> 1): MusicBrainz returns position=1 for single-disc albums
    // by default, and writing an explicit DISC=1 to one track while
    // its sibling tracks have no DISC tag at all causes Navidrome to
    // surface them as separate discs.
    // Song-scope only — album/album_artist live on the album edit
     // modal and shouldn't be touched by a per-track identify.
    setForm(prev => ({
      ...prev,
      title: c.title || prev.title,
      artist: c.artist || prev.artist,
      year: c.year ? String(c.year) : prev.year,
      track_number: c.track_number ? String(c.track_number) : prev.track_number,
      disc_number: c.disc_number && c.disc_number > 1 ? String(c.disc_number) : prev.disc_number,
    }))
    setCandidates(null)
  }

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

        {acoustidAvailable && editable && (
          <div className="modal-identify">
            <button
              type="button"
              className="btn-secondary"
              onClick={handleIdentify}
              disabled={identifying || saving}
            >
              {identifying ? 'Identifying…' : 'Identify with MusicBrainz'}
            </button>
            {candidates && candidates.length > 0 && (
              <ul className="candidate-list">
                {candidates.map(c => (
                  <li key={c.recording_id}>
                    <button type="button" className="candidate-row" onClick={() => applyCandidate(c)}>
                      <span className="candidate-score" title={`AcoustID score: ${c.score.toFixed(2)}`}>
                        {Math.round(c.score * 100)}%
                      </span>
                      <span className="candidate-meta">
                        <span className="candidate-title">{c.title}</span>
                        <span className="candidate-sub">
                          {c.artist}
                          {c.album ? ` · ${c.album}` : ''}
                          {c.year ? ` (${c.year})` : ''}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}

        <form onSubmit={handleSubmit} className="modal-form">
          <label>
            <span>Title</span>
            <input type="text" value={form.title} onChange={e => update('title', e.target.value)} disabled={!editable || saving} />
          </label>
          <label>
            <span>Artist</span>
            <input
              type="text"
              value={form.artist}
              onChange={e => update('artist', e.target.value)}
              placeholder="e.g. Drake feat. 21 Savage"
              disabled={!editable || saving}
            />
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
