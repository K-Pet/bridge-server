import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  getArtists, getAlbumList, getRandomSongs, search,
  coverArtUrl, formatDuration,
  type Artist, type Album, type Song, type SearchResult,
} from '../lib/subsonic'
import {
  getPurchases,
  redeliverPurchase,
  getTrackDownloadURL,
  downloadAlbumZip,
  subscribeEvents,
  deleteSong,
  deleteAlbum,
  type Purchase,
  type PurchaseItem,
  type PurchasedTrack,
  type DeliverySummary,
} from '../lib/api'
import { usePlayer } from '../context/PlayerContext'

type Tab = 'artists' | 'albums' | 'songs' | 'purchases'

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
        {activeTab !== 'purchases' && (
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
        )}
      </div>

      {searchResults ? (
        <SearchResults results={searchResults} searching={searching} />
      ) : (
        <>
          <div className="tab-bar">
            {(['artists', 'albums', 'songs', 'purchases'] as Tab[]).map(tab => (
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
          {activeTab === 'purchases' && <PurchasesTab />}
        </>
      )}
    </div>
  )
}

// ── Search Results ──────────────────────────────────────────────────

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

// ── Artists Tab ──────────────────────────────────────────────────────

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
  if (error || artists.length === 0) return (
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

// ── Albums Tab ───────────────────────────────────────────────────────

function AlbumsTab() {
  const [albums, setAlbums] = useState<Album[]>([])
  const [loading, setLoading] = useState(true)
  const [sortBy, setSortBy] = useState<'newest' | 'alphabeticalByName' | 'alphabeticalByArtist'>('newest')
  const [deleting, setDeleting] = useState<string | null>(null)

  useEffect(() => {
    setLoading(true)
    getAlbumList(sortBy, 100)
      .then(setAlbums)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [sortBy])

  async function handleDeleteAlbum(e: React.MouseEvent, album: Album) {
    e.preventDefault() // Don't navigate to album detail
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
              <span className="card-subtitle">{a.artist}{a.year ? ` · ${a.year}` : ''}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

// ── Songs Tab ────────────────────────────────────────────────────────

function SongsTab() {
  const [songs, setSongs] = useState<Song[]>([])
  const [loading, setLoading] = useState(true)
  const [deleting, setDeleting] = useState<string | null>(null)
  const { playSong } = usePlayer()

  useEffect(() => {
    getRandomSongs(100)
      .then(setSongs)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  async function handleDeleteSong(e: React.MouseEvent, song: Song) {
    e.stopPropagation()
    if (!confirm(`Delete "${song.title}" by ${song.artist}?`)) return
    setDeleting(song.id)
    try {
      await deleteSong(song.id)
      setSongs(prev => prev.filter(s => s.id !== song.id))
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Delete failed')
    } finally {
      setDeleting(null)
    }
  }

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
        <span className="song-actions-header" />
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
          <span
            className="btn-delete-sm"
            role="button"
            tabIndex={0}
            onClick={(e) => handleDeleteSong(e, song)}
            title={`Delete ${song.title}`}
          >
            {deleting === song.id ? (
              <span className="spinner-sm" />
            ) : (
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" /></svg>
            )}
          </span>
        </button>
      ))}
    </div>
  )
}

// ── Purchases Tab ───────────────────────────────────────────────────

function formatPrice(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`
}

function deliveryLabel(d: DeliverySummary | undefined, purchaseStatus: string): string {
  if (!d || d.total === 0) {
    if (purchaseStatus === 'delivered') return 'Delivered'
    if (purchaseStatus === 'failed') return 'Failed'
    if (purchaseStatus === 'delivering') return 'Awaiting server'
    return purchaseStatus
  }
  if (d.all_complete) return 'Delivered'
  if (d.any_failed) return 'Partial failure'
  const counts = d.by_status || {}
  const done = counts.complete || 0
  const downloading = (counts.downloading || 0) + (counts.queued || 0)
  const scanning = counts.scanning || 0
  const written = counts.written || 0
  if (scanning > 0) return `Scanning library (${done}/${d.total})`
  if (written > 0) return `Writing files (${done}/${d.total})`
  if (downloading > 0) return `Downloading (${done}/${d.total})`
  return `${done}/${d.total}`
}

function deliveryVariant(d: DeliverySummary | undefined, purchaseStatus: string): string {
  if (d?.all_complete || purchaseStatus === 'delivered') return 'success'
  if (d?.any_failed || purchaseStatus === 'failed') return 'failed'
  return 'progress'
}

function sortTracks(tracks: PurchasedTrack[]): PurchasedTrack[] {
  return [...tracks].sort((a, b) => {
    const da = a.disc_number ?? 1
    const db = b.disc_number ?? 1
    if (da !== db) return da - db
    return (a.album_index ?? 0) - (b.album_index ?? 0)
  })
}

function triggerBrowserDownload(url: string, filename: string) {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

function PurchasesTab() {
  const [purchases, setPurchases] = useState<Purchase[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [redelivering, setRedelivering] = useState<string | null>(null)
  const [downloading, setDownloading] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [pollTick, setPollTick] = useState(0)

  useEffect(() => {
    let cancelled = false
    getPurchases()
      .then(data => { if (!cancelled) setPurchases(data) })
      .catch(e => { if (!cancelled) setError(e instanceof Error ? e.message : String(e)) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [pollTick])

  useEffect(() => {
    let cleanup: (() => void) | undefined
    let cancelled = false
    subscribeEvents((ev) => {
      if (
        ev.type === 'task_status' ||
        ev.type === 'library_updated' ||
        ev.type === 'purchase_enqueued'
      ) {
        setPollTick((t) => t + 1)
      }
    }).then((unsub) => {
      if (cancelled) { unsub(); return }
      cleanup = unsub
    })
    return () => { cancelled = true; cleanup?.() }
  }, [])

  async function handleRedeliver(purchaseId: string) {
    setRedelivering(purchaseId)
    setNotice(null)
    try {
      const res = await redeliverPurchase(purchaseId)
      if (res.delivery_error) {
        setNotice(`Re-delivery trigger failed: ${res.delivery_error}`)
      } else {
        setNotice('Re-delivery started. Tracks will re-download shortly.')
      }
      setPollTick(t => t + 1)
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err))
    } finally {
      setRedelivering(null)
    }
  }

  async function handleDownload(trackId: string) {
    setDownloading(trackId)
    setNotice(null)
    try {
      const { url, filename } = await getTrackDownloadURL(trackId)
      triggerBrowserDownload(url, filename)
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err))
    } finally {
      setDownloading(null)
    }
  }

  async function handleDownloadAlbum(albumId: string) {
    const groupKey = `album:${albumId}`
    setDownloading(groupKey)
    setNotice(null)
    try {
      await downloadAlbumZip(albumId)
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err))
    } finally {
      setDownloading(null)
    }
  }

  if (loading) return <div className="loading">Loading purchases...</div>
  if (error) return <div className="error-page">Error: {error}</div>

  return (
    <div>
      {notice && (
        <div className="marketplace-toast toast-warning">
          {notice}
          <button className="toast-close" onClick={() => setNotice(null)}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12" /></svg>
          </button>
        </div>
      )}

      {purchases.length === 0 ? (
        <div className="empty-state">
          <p>No purchases yet.</p>
          <p>Visit the Marketplace to buy music.</p>
        </div>
      ) : (
        <div className="purchases-list">
          {purchases.map(p => {
            const variant = deliveryVariant(p.delivery, p.status)
            const label = deliveryLabel(p.delivery, p.status)
            return (
              <div key={p.id} className="purchase-card">
                <div className="purchase-card-header">
                  <div>
                    <div className="purchase-ref">{p.payment_ref || p.id.slice(0, 8)}</div>
                    <div className="purchase-date">{new Date(p.created_at).toLocaleString()}</div>
                  </div>
                  <div className="purchase-card-meta">
                    <span className={`delivery-badge delivery-${variant}`}>{label}</span>
                    <span className="purchase-total">{formatPrice(p.total_cents)}</span>
                    <button
                      className="btn-secondary btn-sm"
                      onClick={() => handleRedeliver(p.id)}
                      disabled={redelivering === p.id}
                      title="Re-trigger delivery to this server"
                    >
                      {redelivering === p.id ? 'Re-delivering...' : 'Re-deliver'}
                    </button>
                  </div>
                </div>

                <div className="purchase-items">
                  {p.purchase_items.map(item => (
                    <PurchaseItemRow
                      key={item.id}
                      item={item}
                      downloading={downloading}
                      onDownload={handleDownload}
                      onDownloadAlbum={handleDownloadAlbum}
                    />
                  ))}
                </div>

                {p.delivery?.any_failed && p.delivery.last_error && (
                  <div className="purchase-card-error">Last error: {p.delivery.last_error}</div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function PurchaseItemRow({
  item,
  downloading,
  onDownload,
  onDownloadAlbum,
}: {
  item: PurchaseItem
  downloading: string | null
  onDownload: (trackId: string) => void
  onDownloadAlbum: (albumId: string) => void
}) {
  if (item.track) {
    const t = item.track
    return (
      <div className="purchase-item purchase-item-track">
        <div className="purchase-item-main">
          <span className="purchase-kind">Track</span>
          <div className="purchase-item-body">
            <span className="purchase-item-title">{t.title}</span>
            <span className="purchase-item-subtitle">{t.artist}</span>
          </div>
          <span className="purchase-item-price">${(item.price_cents / 100).toFixed(2)}</span>
          <button
            className="btn-secondary btn-sm"
            onClick={() => onDownload(t.id)}
            disabled={downloading === t.id}
          >
            {downloading === t.id ? '...' : 'Download'}
          </button>
        </div>
      </div>
    )
  }

  if (item.album) {
    const album = item.album
    const tracks = sortTracks(album.tracks || [])
    const groupKey = `album:${album.id}`
    const anyDownloading = downloading === groupKey
    return (
      <div className="purchase-item purchase-item-album">
        <div className="purchase-item-main">
          <span className="purchase-kind">Album</span>
          <div className="purchase-item-body">
            <span className="purchase-item-title">{album.title}</span>
            <span className="purchase-item-subtitle">
              {album.artist} &middot; {tracks.length} track{tracks.length !== 1 ? 's' : ''}
            </span>
          </div>
          <span className="purchase-item-price">${(item.price_cents / 100).toFixed(2)}</span>
          <button
            className="btn-secondary btn-sm"
            onClick={() => onDownloadAlbum(album.id)}
            disabled={anyDownloading || tracks.length === 0}
            title="Download every track in this album as a zip"
          >
            {anyDownloading ? 'Zipping...' : 'Download zip'}
          </button>
        </div>
        {tracks.length > 0 && (
          <ul className="purchase-item-tracks">
            {tracks.map(t => (
              <li key={t.id} className="purchase-item-track-row">
                <span className="purchase-item-track-num">{t.album_index ?? ''}</span>
                <span className="purchase-item-track-title">{t.title}</span>
                <span className="purchase-item-track-format">{t.format?.toUpperCase()}</span>
                <button
                  className="btn-secondary btn-sm"
                  onClick={() => onDownload(t.id)}
                  disabled={downloading === t.id || anyDownloading}
                >
                  {downloading === t.id ? '...' : 'Download'}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    )
  }

  return (
    <div className="purchase-item">
      <div className="purchase-item-main">
        <span className="purchase-kind">Item</span>
        <div className="purchase-item-body">
          <span className="purchase-item-title">(unknown item)</span>
        </div>
      </div>
    </div>
  )
}
