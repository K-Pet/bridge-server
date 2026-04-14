import { useEffect, useState } from 'react'
import {
  getPurchases,
  redeliverPurchase,
  getTrackDownloadURL,
  downloadAlbumZip,
  subscribeEvents,
  type Purchase,
  type PurchaseItem,
  type PurchasedTrack,
  type DeliverySummary,
} from '../lib/api'

function formatPrice(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`
}

// Turn the raw delivery summary into a human-friendly progress string.
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

// Trigger a native browser download via an <a download> click, given a URL.
function triggerBrowserDownload(url: string, filename: string) {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

export default function Purchases() {
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

  // Live refresh: subscribe to server events once and re-fetch purchases on
  // any delivery-related signal. The SSE channel replaces the 5s poll loop —
  // we only refetch when the server tells us something actually changed.
  useEffect(() => {
    const unsubscribe = subscribeEvents((ev) => {
      if (
        ev.type === 'task_status' ||
        ev.type === 'library_updated' ||
        ev.type === 'purchase_enqueued'
      ) {
        setPollTick((t) => t + 1)
      }
    })
    return unsubscribe
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

  // Stream the entire album as a single zip from the bridge-server.
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
    <div className="purchases-page">
      <h2>Purchase History</h2>

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
  // Track purchase: single-line row
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

  // Album purchase: header row + expanded track rows
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
