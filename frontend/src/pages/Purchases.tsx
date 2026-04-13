import { useEffect, useState } from 'react'
import { getPurchases } from '../lib/api'

interface PurchaseTask {
  id: string
  purchase_id: string
  track: {
    artist: string
    album: string
    title: string
  }
  status: string
  created_at: string
}

export default function Purchases() {
  const [purchases, setPurchases] = useState<PurchaseTask[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    getPurchases()
      .then(data => setPurchases(data as PurchaseTask[]))
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="loading">Loading purchases...</div>

  return (
    <div className="purchases-page">
      <h2>Purchase History</h2>
      {purchases.length === 0 ? (
        <div className="empty-state">
          <p>No purchases yet.</p>
        </div>
      ) : (
        <table className="purchases-table">
          <thead>
            <tr>
              <th>Track</th>
              <th>Artist</th>
              <th>Album</th>
              <th>Status</th>
              <th>Date</th>
            </tr>
          </thead>
          <tbody>
            {purchases.map(p => (
              <tr key={p.id}>
                <td>{p.track.title}</td>
                <td>{p.track.artist}</td>
                <td>{p.track.album}</td>
                <td><span className={`status status-${p.status}`}>{p.status}</span></td>
                <td>{new Date(p.created_at).toLocaleDateString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
