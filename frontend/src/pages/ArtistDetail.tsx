import { useEffect, useRef, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { getArtist, coverArtUrl, findArtistByName, SubsonicNotFoundError, type Artist, type Album } from '../lib/subsonic'
import { deleteAlbum, subscribeEvents, uploadArtistPhoto, type RenameArtistResult } from '../lib/api'
import EditArtistModal from '../components/EditArtistModal'

export default function ArtistDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [artist, setArtist] = useState<Artist | null>(null)
  const [albums, setAlbums] = useState<Album[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  // renameSummary surfaces the server's cascade report ("Renamed 47
  // tracks, kept 8 features intact") so the user understands what
  // happened. Auto-dismisses after a few seconds.
  const [renameSummary, setRenameSummary] = useState<RenameArtistResult | null>(null)
  const [uploadingPhoto, setUploadingPhoto] = useState(false)
  // localPhotoURL is a blob URL of the just-uploaded image. Same role
  // as AlbumDetail.localCoverURL — shows the new photo immediately so
  // the user doesn't think the upload failed while Navidrome's
  // artist-art cache catches up.
  const [localPhotoURL, setLocalPhotoURL] = useState<string | null>(null)
  const photoInputRef = useRef<HTMLInputElement | null>(null)
  // pendingName captures the artist's intended new name across a
  // rescan, so refresh() can find the new id when Navidrome's hash
  // shifts and the URL we're on goes 404.
  const pendingNameRef = useRef<string | null>(null)

  async function refresh() {
    if (!id) return
    try {
      const result = await getArtist(id)
      setArtist(result.artist)
      setAlbums(result.albums)
      // Hold the pending name until Navidrome reports the new one —
      // see the matching comment in AlbumDetail.refresh for the race
      // we're guarding against (post-save refresh returns stale data
      // before the rescan completes).
      const pending = pendingNameRef.current
      if (pending && result.artist.name.trim().toLowerCase() === pending.trim().toLowerCase()) {
        pendingNameRef.current = null
      }
    } catch (e) {
      if (e instanceof SubsonicNotFoundError) {
        const candidates: string[] = []
        if (pendingNameRef.current) candidates.push(pendingNameRef.current)
        if (artist?.name) candidates.push(artist.name)
        for (const name of candidates) {
          try {
            const found = await findArtistByName(name)
            if (found) {
              pendingNameRef.current = null
              navigate(`/artist/${found.id}`, { replace: true })
              return
            }
          } catch { /* try next */ }
        }
        navigate('/', { replace: true })
        return
      }
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getArtist(id)
      .then(({ artist, albums }) => {
        setArtist(artist)
        setAlbums(albums)
      })
      .catch(e => {
        if (e instanceof SubsonicNotFoundError) {
          navigate('/', { replace: true })
          return
        }
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => setLoading(false))
  }, [id])

  // Refresh when Navidrome reports a library update so artist
  // renames propagate without a manual reload.
  useEffect(() => {
    let cancelled = false
    let unsubscribe: (() => void) | undefined
    subscribeEvents(ev => {
      if (cancelled) return
      if (ev.type === 'library_updated') refresh()
    }).then(unsub => {
      if (cancelled) unsub()
      else unsubscribe = unsub
    })
    return () => {
      cancelled = true
      unsubscribe?.()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  async function handlePhotoFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file || !id) return
    if (!['image/jpeg', 'image/png'].includes(file.type)) {
      alert('Artist photo must be a JPEG or PNG image.')
      return
    }
    setUploadingPhoto(true)
    try {
      await uploadArtistPhoto(id, file)
      const url = URL.createObjectURL(file)
      setLocalPhotoURL(prev => {
        if (prev) URL.revokeObjectURL(prev)
        return url
      })
      refresh()
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Artist photo upload failed')
    } finally {
      setUploadingPhoto(false)
    }
  }

  // Revoke the blob URL when this component unmounts or the URL is
  // replaced by a fresh upload — short-lived but the browser doesn't
  // reclaim them automatically.
  useEffect(() => {
    return () => {
      if (localPhotoURL) URL.revokeObjectURL(localPhotoURL)
    }
  }, [localPhotoURL])

  // Auto-dismiss the rename summary after a few seconds. Long enough
  // to read, short enough not to clutter when the user keeps working.
  useEffect(() => {
    if (!renameSummary) return
    const t = setTimeout(() => setRenameSummary(null), 6000)
    return () => clearTimeout(t)
  }, [renameSummary])

  async function handleDeleteAlbum(e: React.MouseEvent, album: Album) {
    e.preventDefault()
    e.stopPropagation()
    if (!confirm(`Delete "${album.name}" by ${album.artist}? This removes all files from your library.`)) return
    setDeleting(album.id)
    try {
      await deleteAlbum(album.id)
      setAlbums(prev => prev.filter(a => a.id !== album.id))
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Delete failed')
    } finally {
      setDeleting(null)
    }
  }

  if (loading) return <div className="loading">Loading artist...</div>
  if (error) return <div className="error-page">Error: {error}</div>
  if (!artist) return <div className="error-page">Artist not found</div>

  return (
    <div className="detail-page">
      <button className="back-link" onClick={() => navigate(-1)}>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M19 12H5M12 19l-7-7 7-7" /></svg>
        Back
      </button>

      {renameSummary && (
        <div className="rename-summary" role="status">
          <div className="rename-summary-icon">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14" /><polyline points="22 4 12 14.01 9 11.01" /></svg>
          </div>
          <div className="rename-summary-body">
            <div className="rename-summary-title">
              Renamed <strong>{renameSummary.old_name}</strong> → <strong>{renameSummary.new_name}</strong>
            </div>
            <div className="rename-summary-detail">
              {renameSummary.renamed_track_count} {renameSummary.renamed_track_count === 1 ? 'track' : 'tracks'} renamed
              {renameSummary.feature_preserved_count > 0 && (
                <> · {renameSummary.feature_preserved_count} {renameSummary.feature_preserved_count === 1 ? 'feature' : 'features'} preserved</>
              )}
              {renameSummary.failed_count > 0 && (
                <> · <span className="rename-summary-failed">{renameSummary.failed_count} failed</span></>
              )}
            </div>
          </div>
          <button
            type="button"
            className="rename-summary-close"
            onClick={() => setRenameSummary(null)}
            aria-label="Dismiss"
          >×</button>
        </div>
      )}

      <div className="artist-hero">
        <button
          type="button"
          className="artist-hero-avatar artist-hero-avatar-editable album-hero-cover-editable"
          onClick={() => photoInputRef.current?.click()}
          disabled={uploadingPhoto}
          title="Click to change photo"
        >
          {localPhotoURL ? (
            <img src={localPhotoURL} alt={artist.name} />
          ) : artist.coverArt ? (
            <img src={coverArtUrl(artist.coverArt, 300)} alt={artist.name} />
          ) : (
            <div className="avatar-placeholder large">
              <svg width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z" /></svg>
            </div>
          )}
          <span className="album-hero-cover-overlay" aria-hidden="true">
            {uploadingPhoto ? (
              <span className="spinner-sm" />
            ) : (
              <>
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 7h4l2-3h6l2 3h4v13H3z" /><circle cx="12" cy="13" r="4" /></svg>
                <span>Change photo</span>
              </>
            )}
          </span>
        </button>
        <input
          ref={photoInputRef}
          type="file"
          accept="image/jpeg,image/png"
          style={{ display: 'none' }}
          onChange={handlePhotoFile}
        />
        <div className="artist-hero-info">
          <span className="detail-label">Artist</span>
          <h1>{artist.name}</h1>
          <p className="detail-meta">{artist.albumCount} {artist.albumCount === 1 ? 'album' : 'albums'}</p>
          <div className="album-actions">
            <button
              className="btn-secondary"
              onClick={() => setEditing(true)}
              title="Rename artist across all tracks"
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M12 20h9" /><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z" /></svg>
              Rename
            </button>
          </div>
        </div>
      </div>

      <h3>Albums</h3>
      {albums.length === 0 ? (
        <div className="empty-state"><p>No albums found.</p></div>
      ) : (
        <div className="album-grid">
          {albums.map(a => (
            <Link key={a.id} to={`/album/${a.id}`} className="album-card">
              <div className="album-cover">
                {a.coverArt ? (
                  <img src={coverArtUrl(a.coverArt)} alt={a.name} loading="lazy" />
                ) : (
                  <div className="cover-placeholder">
                    <svg width="40" height="40" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
                  </div>
                )}
                <div className="album-play-overlay">
                  <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
                </div>
              </div>
              <div className="card-title-row">
                <span className="card-title">{a.name}</span>
                <button
                  className="btn-delete-sm"
                  onClick={(e) => handleDeleteAlbum(e, a)}
                  disabled={deleting === a.id}
                  title={`Delete ${a.name}`}
                >
                  {deleting === a.id ? (
                    <span className="spinner-sm" />
                  ) : (
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
                  )}
                </button>
              </div>
              <span className="card-subtitle">{a.year ? String(a.year) : ''} · {a.songCount} tracks</span>
            </Link>
          ))}
        </div>
      )}

      {editing && artist && (
        <EditArtistModal
          artist={artist}
          onClose={() => setEditing(false)}
          onSaved={(newName, result) => {
            pendingNameRef.current = newName
            setRenameSummary(result)
            refresh()
          }}
        />
      )}
    </div>
  )
}
