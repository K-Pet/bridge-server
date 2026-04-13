import { useEffect, useState } from 'react'
import { getSupabase } from '../lib/supabase'
import { purchaseAlbum, purchaseTrack, type PurchaseResult } from '../lib/api'

interface CatalogAlbum {
  id: string
  title: string
  artist: string
  cover_art_url: string | null
  price_cents: number | null
  release_date: string | null
}

interface CatalogTrack {
  id: string
  title: string
  artist: string
  album_id: string | null
  disc_number: number | null
  album_index: number | null
  format: string | null
  price_cents: number | null
}

interface Purchase {
  id: string
  total_cents: number
  status: string
  payment_ref: string | null
  created_at: string
  purchase_items: { id: string; track_id: string | null; album_id: string | null; price_cents: number }[]
}

function formatPrice(cents: number | null): string {
  if (cents == null) return 'N/A'
  return `$${(cents / 100).toFixed(2)}`
}

export default function Marketplace() {
  const [albums, setAlbums] = useState<CatalogAlbum[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedAlbum, setSelectedAlbum] = useState<CatalogAlbum | null>(null)
  const [tracks, setTracks] = useState<CatalogTrack[]>([])
  const [loadingTracks, setLoadingTracks] = useState(false)
  const [purchases, setPurchases] = useState<Purchase[]>([])
  const [buying, setBuying] = useState<string | null>(null)
  const [lastResult, setLastResult] = useState<PurchaseResult | null>(null)

  // Fetch catalog albums from Supabase
  useEffect(() => {
    const supabase = getSupabase()
    if (!supabase) {
      setLoading(false)
      return
    }

    supabase
      .from('albums')
      .select('id, title, artist, cover_art_url, price_cents, release_date')
      .not('price_cents', 'is', null)
      .order('artist')
      .then(({ data, error }) => {
        if (!error && data) setAlbums(data)
        setLoading(false)
      })
  }, [])

  // Fetch recent purchases
  useEffect(() => {
    refreshPurchases()
  }, [])

  function refreshPurchases() {
    const supabase = getSupabase()
    if (!supabase) return

    supabase
      .from('purchases')
      .select('id, total_cents, status, payment_ref, created_at, purchase_items(id, track_id, album_id, price_cents)')
      .order('created_at', { ascending: false })
      .limit(10)
      .then(({ data }) => {
        if (data) setPurchases(data as Purchase[])
      })
  }

  // Fetch tracks for selected album
  function selectAlbum(album: CatalogAlbum) {
    setSelectedAlbum(album)
    setLoadingTracks(true)

    const supabase = getSupabase()
    if (!supabase) return

    supabase
      .from('tracks')
      .select('id, title, artist, album_id, disc_number, album_index, format, price_cents')
      .eq('album_id', album.id)
      .order('disc_number')
      .order('album_index')
      .then(({ data, error }) => {
        if (!error && data) setTracks(data)
        setLoadingTracks(false)
      })
  }

  async function handleBuyAlbum(album: CatalogAlbum) {
    setBuying(album.id)
    setLastResult(null)
    try {
      const result = await purchaseAlbum(album.id)
      setLastResult(result)
      refreshPurchases()
    } catch (err) {
      setLastResult({ purchase_id: '', status: 'error', delivery_error: String(err) })
    } finally {
      setBuying(null)
    }
  }

  async function handleBuyTrack(track: CatalogTrack) {
    setBuying(track.id)
    setLastResult(null)
    try {
      const result = await purchaseTrack(track.id)
      setLastResult(result)
      refreshPurchases()
    } catch (err) {
      setLastResult({ purchase_id: '', status: 'error', delivery_error: String(err) })
    } finally {
      setBuying(null)
    }
  }

  if (loading) return <div className="loading">Loading catalog...</div>

  return (
    <div className="marketplace-page">
      <h2>Marketplace</h2>

      {lastResult && (
        <div className={`marketplace-toast ${lastResult.delivery_error ? 'toast-warning' : 'toast-success'}`}>
          {lastResult.delivery_error ? (
            <>
              <strong>Purchase created</strong> but delivery failed: {lastResult.delivery_error}
              <br />
              <span className="toast-sub">Purchase ID: {lastResult.purchase_id}</span>
            </>
          ) : (
            <>
              <strong>Purchase successful!</strong> Tracks are being delivered to your server.
              <br />
              <span className="toast-sub">Purchase ID: {lastResult.purchase_id}</span>
            </>
          )}
          <button className="toast-close" onClick={() => setLastResult(null)}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12" /></svg>
          </button>
        </div>
      )}

      {selectedAlbum ? (
        <AlbumDetail
          album={selectedAlbum}
          tracks={tracks}
          loading={loadingTracks}
          buying={buying}
          onBuyAlbum={handleBuyAlbum}
          onBuyTrack={handleBuyTrack}
          onBack={() => { setSelectedAlbum(null); setTracks([]) }}
        />
      ) : (
        <>
          {albums.length === 0 ? (
            <div className="empty-state">
              <p>No albums available for purchase.</p>
              <p>Run the seed script to populate the catalog.</p>
            </div>
          ) : (
            <div className="album-grid">
              {albums.map(album => (
                <button
                  key={album.id}
                  className="album-card marketplace-album-card"
                  onClick={() => selectAlbum(album)}
                >
                  <div className="album-cover">
                    {album.cover_art_url ? (
                      <img src={album.cover_art_url} alt={album.title} loading="lazy" />
                    ) : (
                      <div className="cover-placeholder">
                        <svg width="40" height="40" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
                      </div>
                    )}
                    <div className="marketplace-price-badge">
                      {formatPrice(album.price_cents)}
                    </div>
                  </div>
                  <span className="card-title">{album.title}</span>
                  <span className="card-subtitle">{album.artist}</span>
                </button>
              ))}
            </div>
          )}
        </>
      )}

      {purchases.length > 0 && (
        <div className="marketplace-purchases">
          <div className="marketplace-purchases-header">
            <h3>Recent Purchases</h3>
            <button className="btn-secondary btn-sm" onClick={refreshPurchases}>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M21 2v6h-6" /><path d="M3 12a9 9 0 0 1 15-6.7L21 8" /><path d="M3 22v-6h6" /><path d="M21 12a9 9 0 0 1-15 6.7L3 16" /></svg>
              Refresh
            </button>
          </div>
          <table className="purchases-table">
            <thead>
              <tr>
                <th>Purchase</th>
                <th>Total</th>
                <th>Status</th>
                <th>Date</th>
              </tr>
            </thead>
            <tbody>
              {purchases.map(p => (
                <tr key={p.id}>
                  <td className="purchase-ref">{p.payment_ref || p.id.slice(0, 8)}</td>
                  <td>{formatPrice(p.total_cents)}</td>
                  <td><span className={`status status-${p.status}`}>{p.status}</span></td>
                  <td>{new Date(p.created_at).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function AlbumDetail({
  album, tracks, loading, buying, onBuyAlbum, onBuyTrack, onBack,
}: {
  album: CatalogAlbum
  tracks: CatalogTrack[]
  loading: boolean
  buying: string | null
  onBuyAlbum: (album: CatalogAlbum) => void
  onBuyTrack: (track: CatalogTrack) => void
  onBack: () => void
}) {
  // Group tracks by disc number
  const discs = new Map<number, CatalogTrack[]>()
  for (const t of tracks) {
    const disc = t.disc_number ?? 1
    if (!discs.has(disc)) discs.set(disc, [])
    discs.get(disc)!.push(t)
  }
  const multiDisc = discs.size > 1

  return (
    <div className="detail-page">
      <button className="back-link" onClick={onBack}>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="15 18 9 12 15 6" /></svg>
        Back to Catalog
      </button>

      <div className="album-hero">
        <div className="album-hero-cover">
          {album.cover_art_url ? (
            <img src={album.cover_art_url} alt={album.title} />
          ) : (
            <div className="cover-placeholder large">
              <svg width="64" height="64" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z" /></svg>
            </div>
          )}
        </div>
        <div className="album-hero-info">
          <span className="detail-label">Album</span>
          <h1>{album.title}</h1>
          <div className="detail-meta">
            {album.artist}
            {album.release_date && ` \u00B7 ${new Date(album.release_date).getFullYear()}`}
            {` \u00B7 ${tracks.length} track${tracks.length !== 1 ? 's' : ''}`}
          </div>
          <div className="album-actions">
            <button
              className="btn-primary"
              onClick={() => onBuyAlbum(album)}
              disabled={buying !== null}
            >
              {buying === album.id ? (
                <>
                  <span className="spinner-sm" />
                  Purchasing...
                </>
              ) : (
                <>
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M6 2L3 6v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6l-3-4z" /><line x1="3" y1="6" x2="21" y2="6" /><path d="M16 10a4 4 0 0 1-8 0" /></svg>
                  Buy Album {formatPrice(album.price_cents)}
                </>
              )}
            </button>
          </div>
        </div>
      </div>

      {loading ? (
        <div className="loading">Loading tracks...</div>
      ) : (
        <div className="song-list">
          <div className="song-list-header">
            <span className="song-num">#</span>
            <span className="song-info">Title</span>
            <span className="marketplace-track-format">Format</span>
            <span className="marketplace-track-price">Price</span>
            <span className="marketplace-track-action"></span>
          </div>
          {[...discs.entries()].map(([discNum, discTracks]) => (
            <div key={discNum}>
              {multiDisc && (
                <div className="disc-header">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><circle cx="12" cy="12" r="10" opacity="0.3" /><circle cx="12" cy="12" r="3" /></svg>
                  Disc {discNum}
                </div>
              )}
              {discTracks.map((track) => (
                <div key={track.id} className="song-row marketplace-track-row">
                  <span className="song-num">{track.album_index}</span>
                  <div className="song-info">
                    <span className="song-title">{track.title}</span>
                    <span className="song-meta">{track.artist}</span>
                  </div>
                  <span className="marketplace-track-format">{track.format?.toUpperCase()}</span>
                  <span className="marketplace-track-price">{formatPrice(track.price_cents)}</span>
                  <span className="marketplace-track-action">
                    {track.price_cents != null && (
                      <button
                        className="btn-buy-track"
                        onClick={() => onBuyTrack(track)}
                        disabled={buying !== null}
                      >
                        {buying === track.id ? '...' : 'Buy'}
                      </button>
                    )}
                  </span>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
