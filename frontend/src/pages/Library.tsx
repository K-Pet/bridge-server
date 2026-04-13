import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  getArtists, getAlbumList, getRandomSongs, search,
  coverArtUrl, formatDuration,
  type Artist, type Album, type Song, type SearchResult,
} from '../lib/subsonic'
import { usePlayer } from '../context/PlayerContext'

type Tab = 'artists' | 'albums' | 'songs'

export default function Library() {
  const [searchParams, setSearchParams] = useSearchParams()
  const activeTab = (searchParams.get('tab') as Tab) || 'artists'
  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState<SearchResult | null>(null)
  const [searching, setSearching] = useState(false)

  function setTab(tab: Tab) {
    setSearchParams({ tab })
    setSearchResults(null)
    setQuery('')
  }

  async function handleSearch(q: string) {
    setQuery(q)
    if (q.length < 2) {
      setSearchResults(null)
      return
    }
    setSearching(true)
    try {
      const results = await search(q)
      setSearchResults(results)
    } catch {
      setSearchResults(null)
    } finally {
      setSearching(false)
    }
  }

  return (
    <div className="library-page">
      <div className="library-header">
        <h2>Library</h2>
        <div className="search-bar">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" />
          </svg>
          <input
            type="text"
            placeholder="Search artists, albums, songs..."
            value={query}
            onChange={e => handleSearch(e.target.value)}
          />
        </div>
      </div>

      {searchResults ? (
        <SearchResults results={searchResults} searching={searching} />
      ) : (
        <>
          <div className="tab-bar">
            {(['artists', 'albums', 'songs'] as Tab[]).map(tab => (
              <button
                key={tab}
                className={`tab ${activeTab === tab ? 'active' : ''}`}
                onClick={() => setTab(tab)}
              >
                {tab.charAt(0).toUpperCase() + tab.slice(1)}
              </button>
            ))}
          </div>
          {activeTab === 'artists' && <ArtistsTab />}
          {activeTab === 'albums' && <AlbumsTab />}
          {activeTab === 'songs' && <SongsTab />}
        </>
      )}
    </div>
  )
}

function SearchResults({ results, searching }: { results: SearchResult; searching: boolean }) {
  const { playSong } = usePlayer()
  const hasResults = (results.artist?.length ?? 0) + (results.album?.length ?? 0) + (results.song?.length ?? 0) > 0

  if (searching) return <div className="loading">Searching...</div>
  if (!hasResults) return <div className="empty-state"><p>No results found.</p></div>

  return (
    <div className="search-results">
      {results.artist && results.artist.length > 0 && (
        <section>
          <h3>Artists</h3>
          <div className="artist-grid">
            {results.artist.map(a => (
              <Link key={a.id} to={`/artist/${a.id}`} className="artist-card">
                <div className="artist-avatar">
                  {a.coverArt ? (
                    <img src={coverArtUrl(a.coverArt, 200)} alt={a.name} />
                  ) : (
                    <div className="avatar-placeholder">
                      <svg width="32" height="32" viewBox="0 0 24 24" fill="currentColor"><path d="M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z" /></svg>
                    </div>
                  )}
                </div>
                <span className="card-title">{a.name}</span>
              </Link>
            ))}
          </div>
        </section>
      )}

      {results.album && results.album.length > 0 && (
        <section>
          <h3>Albums</h3>
          <div className="album-grid">
            {results.album.map(a => (
              <Link key={a.id} to={`/album/${a.id}`} className="album-card">
                <div className="album-cover">
                  {a.coverArt ? (
                    <img src={coverArtUrl(a.coverArt)} alt={a.name} loading="lazy" />
                  ) : (
                    <div className="cover-placeholder">
                      <svg width="40" height="40" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
                    </div>
                  )}
                </div>
                <span className="card-title">{a.name}</span>
                <span className="card-subtitle">{a.artist}</span>
              </Link>
            ))}
          </div>
        </section>
      )}

      {results.song && results.song.length > 0 && (
        <section>
          <h3>Songs</h3>
          <div className="song-list">
            {results.song.map((song, i) => (
              <button key={song.id} className="song-row" onClick={() => playSong(song, results.song)}>
                <span className="song-num">{i + 1}</span>
                <div className="song-info">
                  <span className="song-title">{song.title}</span>
                  <span className="song-meta">{song.artist} — {song.album}</span>
                </div>
                <span className="song-duration">{formatDuration(song.duration)}</span>
              </button>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

function ArtistsTab() {
  const [artists, setArtists] = useState<Artist[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    getArtists()
      .then(setArtists)
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="loading">Loading artists...</div>
  if (error) return <div className="error-page">Error: {error}</div>
  if (artists.length === 0) return (
    <div className="empty-state">
      <div className="empty-icon">
        <svg width="48" height="48" viewBox="0 0 24 24" fill="currentColor" opacity="0.3"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
      </div>
      <p>Your library is empty.</p>
      <p>Purchase music from the Bridge Music app to see it here.</p>
    </div>
  )

  return (
    <div className="artist-grid">
      {artists.map(a => (
        <Link key={a.id} to={`/artist/${a.id}`} className="artist-card">
          <div className="artist-avatar">
            {a.coverArt ? (
              <img src={coverArtUrl(a.coverArt, 200)} alt={a.name} loading="lazy" />
            ) : (
              <div className="avatar-placeholder">
                <svg width="32" height="32" viewBox="0 0 24 24" fill="currentColor"><path d="M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z" /></svg>
              </div>
            )}
          </div>
          <span className="card-title">{a.name}</span>
          <span className="card-subtitle">{a.albumCount} {a.albumCount === 1 ? 'album' : 'albums'}</span>
        </Link>
      ))}
    </div>
  )
}

function AlbumsTab() {
  const [albums, setAlbums] = useState<Album[]>([])
  const [loading, setLoading] = useState(true)
  const [sortBy, setSortBy] = useState<'newest' | 'alphabeticalByName' | 'alphabeticalByArtist'>('newest')

  useEffect(() => {
    setLoading(true)
    getAlbumList(sortBy, 100)
      .then(setAlbums)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [sortBy])

  return (
    <div>
      <div className="sort-bar">
        <select value={sortBy} onChange={e => setSortBy(e.target.value as typeof sortBy)}>
          <option value="newest">Recently Added</option>
          <option value="alphabeticalByName">A-Z (Album)</option>
          <option value="alphabeticalByArtist">A-Z (Artist)</option>
        </select>
      </div>
      {loading ? (
        <div className="loading">Loading albums...</div>
      ) : albums.length === 0 ? (
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
              <span className="card-subtitle">{a.artist}{a.year ? ` · ${a.year}` : ''}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

function SongsTab() {
  const [songs, setSongs] = useState<Song[]>([])
  const [loading, setLoading] = useState(true)
  const { playSong } = usePlayer()

  useEffect(() => {
    getRandomSongs(100)
      .then(setSongs)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="loading">Loading songs...</div>
  if (songs.length === 0) return <div className="empty-state"><p>No songs found.</p></div>

  return (
    <div className="song-list">
      <div className="song-list-header">
        <span className="song-num">#</span>
        <span className="song-info">Title</span>
        <span className="song-duration">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" /></svg>
        </span>
      </div>
      {songs.map((song, i) => (
        <button key={song.id} className="song-row" onClick={() => playSong(song, songs)}>
          <span className="song-num">{i + 1}</span>
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
      ))}
    </div>
  )
}
