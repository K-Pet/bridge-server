import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { getPlaylist, coverArtUrl, formatDuration, formatDurationLong, type PlaylistWithSongs } from '../lib/subsonic'
import { usePlayer } from '../context/PlayerContext'

export default function PlaylistDetail() {
  const { id } = useParams<{ id: string }>()
  const [playlist, setPlaylist] = useState<PlaylistWithSongs | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const { playSong, playAlbum, currentSong, isPlaying } = usePlayer()

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getPlaylist(id)
      .then(setPlaylist)
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

  if (loading) return <div className="loading">Loading playlist...</div>
  if (error) return <div className="error-page">Error: {error}</div>
  if (!playlist) return <div className="error-page">Playlist not found</div>

  const songs = playlist.entry ?? []

  return (
    <div className="detail-page">
      <Link to="/playlists" className="back-link">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M19 12H5M12 19l-7-7 7-7" /></svg>
        Playlists
      </Link>

      <div className="album-hero">
        <div className="album-hero-cover">
          {playlist.coverArt ? (
            <img src={coverArtUrl(playlist.coverArt, 400)} alt={playlist.name} />
          ) : (
            <div className="cover-placeholder large">
              <svg width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M15 6H3v2h12V6zm0 4H3v2h12v-2zM3 16h8v-2H3v2zM17 6v8.18c-.31-.11-.65-.18-1-.18-1.66 0-3 1.34-3 3s1.34 3 3 3 3-1.34 3-3V8h3V6h-5z" /></svg>
            </div>
          )}
        </div>
        <div className="album-hero-info">
          <span className="detail-label">Playlist</span>
          <h1>{playlist.name}</h1>
          <p className="detail-meta">
            {playlist.songCount} songs · {formatDurationLong(playlist.duration)}
          </p>
          {playlist.comment && <p className="detail-comment">{playlist.comment}</p>}
          <div className="album-actions">
            <button className="btn-primary" onClick={() => playAlbum(songs)}>
              <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
              Play
            </button>
            <button className="btn-secondary" onClick={() => {
              const shuffled = [...songs].sort(() => Math.random() - 0.5)
              playAlbum(shuffled)
            }}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="16 3 21 3 21 8" /><line x1="4" y1="20" x2="21" y2="3" /><polyline points="21 16 21 21 16 21" /><line x1="15" y1="15" x2="21" y2="21" /><line x1="4" y1="4" x2="9" y2="9" /></svg>
              Shuffle
            </button>
          </div>
        </div>
      </div>

      <div className="song-list">
        <div className="song-list-header">
          <span className="song-num">#</span>
          <span className="song-info">Title</span>
          <span className="song-duration">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" /></svg>
          </span>
        </div>
        {songs.map((song, i) => {
          const active = currentSong?.id === song.id
          return (
            <button
              key={`${song.id}-${i}`}
              className={`song-row ${active ? 'active' : ''}`}
              onClick={() => playSong(song, songs)}
            >
              <span className="song-num">
                {active && isPlaying ? (
                  <span className="playing-indicator">
                    <span /><span /><span />
                  </span>
                ) : (
                  i + 1
                )}
              </span>
              <div className="song-cover-small">
                {song.coverArt ? (
                  <img src={coverArtUrl(song.coverArt, 40)} alt="" />
                ) : (
                  <div className="cover-placeholder-sm" />
                )}
              </div>
              <div className="song-info">
                <span className="song-title">{song.title}</span>
                <span className="song-meta">{song.artist} — {song.album}</span>
              </div>
              <span className="song-duration">{formatDuration(song.duration)}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
