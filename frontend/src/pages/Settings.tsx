import { useEffect, useState } from 'react'
import { getHealth, getSettings } from '../lib/api'

export default function Settings() {
  const [health, setHealth] = useState<string>('')
  const [settings, setSettings] = useState<{ delivery_mode: string; poll_interval: string } | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    Promise.all([
      getHealth().then(h => setHealth(h.status)).catch(() => setHealth('unreachable')),
      getSettings().then(setSettings).catch(() => {}),
    ]).finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="loading">Loading settings...</div>

  return (
    <div className="settings-page">
      <h2>Server Settings</h2>

      <section className="settings-section">
        <h3>Status</h3>
        <div className="setting-row">
          <span className="setting-label">Server Health</span>
          <span className={`status status-${health === 'ok' ? 'complete' : 'failed'}`}>
            {health}
          </span>
        </div>
      </section>

      {settings && (
        <section className="settings-section">
          <h3>Delivery</h3>
          <div className="setting-row">
            <span className="setting-label">Mode</span>
            <span>{settings.delivery_mode}</span>
          </div>
          <div className="setting-row">
            <span className="setting-label">Poll Interval</span>
            <span>{settings.poll_interval}</span>
          </div>
        </section>
      )}
    </div>
  )
}
