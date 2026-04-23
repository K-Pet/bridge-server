import { useEffect, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { getArtist, coverArtUrl, type Artist, type Album } from '../lib/subsonic'
import { deleteAlbum } from '../lib/api'

export default function ArtistDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [artist, setArtist] = useState<Artist | null>(null)
  const [albums, setAlbums] = useState<Album[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getArtist(id)
      .then(({ artist, albums }) => {
        setArtist(artist)
        setAlbums(albums)
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

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

      <div className="artist-hero">
        <div className="artist-hero-avatar">
          {artist.coverArt ? (
            <img src={coverArtUrl(artist.coverArt, 300)} alt={artist.name} />
          ) : (
            <div className="avatar-placeholder large">
              <svg width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z" /></svg>
            </div>
          )}
        </div>
        <div className="artist-hero-info">
          <span className="detail-label">Artist</span>
          <h1>{artist.name}</h1>
          <p className="detail-meta">{artist.albumCount} {artist.albumCount === 1 ? 'album' : 'albums'}</p>
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
    </div>
  )
}
