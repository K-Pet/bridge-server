import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getPlaylists, coverArtUrl, formatDurationLong, type Playlist } from '../lib/subsonic'

export default function Playlists() {
  const [playlists, setPlaylists] = useState<Playlist[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    getPlaylists()
      .then(setPlaylists)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="loading">Loading playlists...</div>

  return (
    <div className="library-page">
      <h2>Playlists</h2>
      {playlists.length === 0 ? (
        <div className="empty-state">
          <div className="empty-icon">
            <svg width="48" height="48" viewBox="0 0 24 24" fill="currentColor" opacity="0.3"><path d="M15 6H3v2h12V6zm0 4H3v2h12v-2zM3 16h8v-2H3v2zM17 6v8.18c-.31-.11-.65-.18-1-.18-1.66 0-3 1.34-3 3s1.34 3 3 3 3-1.34 3-3V8h3V6h-5z" /></svg>
          </div>
          <p>No playlists yet.</p>
          <p>Create playlists to organize your music.</p>
        </div>
      ) : (
        <div className="playlist-grid">
          {playlists.map(pl => (
            <Link key={pl.id} to={`/playlist/${pl.id}`} className="playlist-card">
              <div className="playlist-cover">
                {pl.coverArt ? (
                  <img src={coverArtUrl(pl.coverArt)} alt={pl.name} loading="lazy" />
                ) : (
                  <div className="cover-placeholder">
                    <svg width="40" height="40" viewBox="0 0 24 24" fill="currentColor"><path d="M15 6H3v2h12V6zm0 4H3v2h12v-2zM3 16h8v-2H3v2zM17 6v8.18c-.31-.11-.65-.18-1-.18-1.66 0-3 1.34-3 3s1.34 3 3 3 3-1.34 3-3V8h3V6h-5z" /></svg>
                  </div>
                )}
              </div>
              <span className="card-title">{pl.name}</span>
              <span className="card-subtitle">{pl.songCount} songs · {formatDurationLong(pl.duration)}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}
