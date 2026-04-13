import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { getArtist, coverArtUrl, type Artist, type Album } from '../lib/subsonic'

export default function ArtistDetail() {
  const { id } = useParams<{ id: string }>()
  const [artist, setArtist] = useState<Artist | null>(null)
  const [albums, setAlbums] = useState<Album[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

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

  if (loading) return <div className="loading">Loading artist...</div>
  if (error) return <div className="error-page">Error: {error}</div>
  if (!artist) return <div className="error-page">Artist not found</div>

  return (
    <div className="detail-page">
      <Link to="/" className="back-link">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M19 12H5M12 19l-7-7 7-7" /></svg>
        Library
      </Link>

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
              <span className="card-title">{a.name}</span>
              <span className="card-subtitle">{a.year ? String(a.year) : ''} · {a.songCount} tracks</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}
