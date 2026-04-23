import { useEffect, useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { getAlbum, coverArtUrl, formatDuration, formatDurationLong, type Album, type Song } from '../lib/subsonic'
import { deleteSong, deleteAlbum } from '../lib/api'
import { usePlayer } from '../context/PlayerContext'

export default function AlbumDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [album, setAlbum] = useState<Album | null>(null)
  const [songs, setSongs] = useState<Song[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [deletingAlbum, setDeletingAlbum] = useState(false)
  const [deletingSong, setDeletingSong] = useState<string | null>(null)
  const { playSong, playAlbum, currentSong, isPlaying } = usePlayer()

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getAlbum(id)
      .then(({ album, songs }) => {
        setAlbum(album)
        setSongs(songs)
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

  async function handleDeleteAlbum() {
    if (!album || !id) return
    if (!confirm(`Delete "${album.name}" by ${album.artist}? This removes all files from your library.`)) return
    setDeletingAlbum(true)
    try {
      await deleteAlbum(id)
      navigate('/')
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Delete failed')
      setDeletingAlbum(false)
    }
  }

  async function handleDeleteSong(e: React.MouseEvent, song: Song) {
    e.stopPropagation()
    if (!confirm(`Delete "${song.title}" by ${song.artist}?`)) return
    setDeletingSong(song.id)
    try {
      await deleteSong(song.id)
      setSongs(prev => prev.filter(s => s.id !== song.id))
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Delete failed')
    } finally {
      setDeletingSong(null)
    }
  }

  if (loading) return <div className="loading">Loading album...</div>
  if (error) return <div className="error-page">Error: {error}</div>
  if (!album) return <div className="error-page">Album not found</div>

  const totalDuration = songs.reduce((sum, s) => sum + s.duration, 0)
  const isCurrentAlbum = currentSong && songs.some(s => s.id === currentSong.id)

  const discNumbers = [...new Set(songs.map(s => s.discNumber ?? 1))].sort((a, b) => a - b)
  const isMultiDisc = discNumbers.length > 1

  return (
    <div className="detail-page">
      <button className="back-link" onClick={() => navigate(-1)}>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M19 12H5M12 19l-7-7 7-7" /></svg>
        Back
      </button>

      <div className="album-hero">
        <div className="album-hero-cover">
          {album.coverArt ? (
            <img src={coverArtUrl(album.coverArt, 400)} alt={album.name} />
          ) : (
            <div className="cover-placeholder large">
              <svg width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
            </div>
          )}
        </div>
        <div className="album-hero-info">
          <span className="detail-label">Album</span>
          <h1>{album.name}</h1>
          <p className="detail-meta">
            <Link to={`/artist/${album.artistId}`} className="meta-link">{album.artist}</Link>
            {album.year ? ` · ${album.year}` : ''}
            {` · ${album.songCount} songs, ${formatDurationLong(totalDuration)}`}
          </p>
          <div className="album-actions">
            <button className="btn-primary" onClick={() => playAlbum(songs)}>
              <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
              {isCurrentAlbum && isPlaying ? 'Playing' : 'Play'}
            </button>
            <button className="btn-secondary" onClick={() => {
              const shuffled = [...songs].sort(() => Math.random() - 0.5)
              playAlbum(shuffled)
            }}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="16 3 21 3 21 8" /><line x1="4" y1="20" x2="21" y2="3" /><polyline points="21 16 21 21 16 21" /><line x1="15" y1="15" x2="21" y2="21" /><line x1="4" y1="4" x2="9" y2="9" /></svg>
              Shuffle
            </button>
            <button
              className="btn-delete"
              onClick={handleDeleteAlbum}
              disabled={deletingAlbum}
              title="Delete album from library"
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
              {deletingAlbum ? 'Deleting...' : 'Delete'}
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
          <span className="song-actions-header" />
        </div>
        {isMultiDisc
          ? discNumbers.flatMap(disc => {
              const discSongs = songs.filter(s => (s.discNumber ?? 1) === disc)
              return [
                <div key={`disc-${disc}`} className="disc-header">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 14c-2.21 0-4-1.79-4-4s1.79-4 4-4 4 1.79 4 4-1.79 4-4 4zm0-6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2z"/></svg>
                  Disc {disc}
                </div>,
                ...discSongs.map((song, i) => {
                  const active = currentSong?.id === song.id
                  return (
                    <button
                      key={song.id}
                      className={`song-row ${active ? 'active' : ''}`}
                      onClick={() => playSong(song, songs)}
                    >
                      <span className="song-num">
                        {active && isPlaying ? (
                          <span className="playing-indicator">
                            <span /><span /><span />
                          </span>
                        ) : (
                          song.track ?? i + 1
                        )}
                      </span>
                      <div className="song-info">
                        <span className="song-title">{song.title}</span>
                      </div>
                      <span className="song-duration">{formatDuration(song.duration)}</span>
                      <span
                        className="btn-delete-sm"
                        role="button"
                        tabIndex={0}
                        onClick={(e) => handleDeleteSong(e, song)}
                        title={`Delete ${song.title}`}
                      >
                        {deletingSong === song.id ? (
                          <span className="spinner-sm" />
                        ) : (
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
                        )}
                      </span>
                    </button>
                  )
                }),
              ]
            })
          : songs.map((song, i) => {
              const active = currentSong?.id === song.id
              return (
                <button
                  key={song.id}
                  className={`song-row ${active ? 'active' : ''}`}
                  onClick={() => playSong(song, songs)}
                >
                  <span className="song-num">
                    {active && isPlaying ? (
                      <span className="playing-indicator">
                        <span /><span /><span />
                      </span>
                    ) : (
                      song.track ?? i + 1
                    )}
                  </span>
                  <div className="song-info">
                    <span className="song-title">{song.title}</span>
                  </div>
                  <span className="song-duration">{formatDuration(song.duration)}</span>
                  <span
                    className="btn-delete-sm"
                    role="button"
                    tabIndex={0}
                    onClick={(e) => handleDeleteSong(e, song)}
                    title={`Delete ${song.title}`}
                  >
                    {deletingSong === song.id ? (
                      <span className="spinner-sm" />
                    ) : (
                      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
                    )}
                  </span>
                </button>
              )
            })}
      </div>
    </div>
  )
}
