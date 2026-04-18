import { useCallback, useEffect, useMemo, useState } from 'react'
import { generatePairCode, getHealth, getSettings, type PairCode } from '../lib/api'

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

      <PairSection />
    </div>
  )
}

// PairSection — mints a one-shot pair code from POST /api/pair/generate.
// The code is valid for 5 minutes; the countdown and "expired" state
// are tracked client-side so we don't need to poll the server.
function PairSection() {
  const [code, setCode] = useState<PairCode | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [now, setNow] = useState(Date.now())

  // Re-render once per second so the countdown label updates live.
  useEffect(() => {
    if (!code) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [code])

  const remaining = useMemo(() => {
    if (!code) return 0
    const diff = new Date(code.expires_at).getTime() - now
    return Math.max(0, Math.floor(diff / 1000))
  }, [code, now])

  const expired = !!code && remaining <= 0

  const onGenerate = useCallback(async () => {
    setBusy(true)
    setErr(null)
    setCopied(false)
    try {
      const next = await generatePairCode()
      setCode(next)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Failed to generate code')
    } finally {
      setBusy(false)
    }
  }, [])

  const onCopy = useCallback(async () => {
    if (!code) return
    try {
      await navigator.clipboard.writeText(code.code)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard write can fail in non-secure contexts; ignore silently.
    }
  }, [code])

  return (
    <section className="settings-section">
      <h3>Pair marketplace app</h3>
      <p className="setting-help">
        Generate a one-shot code, then enter it in the Bridge Music mobile
        app (Account tab) to link this server as your home server. The
        marketplace will send purchase webhooks straight to you.
      </p>

      {code && !expired ? (
        <div className="pair-code-block">
          <div className="pair-code" aria-label="pair code">
            {code.code.split('').map((ch, i) => (
              <span key={i} className="pair-code-digit">{ch}</span>
            ))}
          </div>
          <div className="pair-code-meta">
            <span className="setting-label">
              Expires in {formatSeconds(remaining)}
            </span>
            <div className="pair-code-actions">
              <button type="button" className="btn-secondary" onClick={onCopy}>
                {copied ? 'Copied!' : 'Copy'}
              </button>
              <button
                type="button"
                className="btn-secondary"
                onClick={onGenerate}
                disabled={busy}
              >
                {busy ? 'Generating…' : 'New code'}
              </button>
            </div>
          </div>
        </div>
      ) : (
        <div className="pair-code-empty">
          {expired && (
            <p className="setting-help pair-expired">
              The previous code has expired. Generate a new one to pair.
            </p>
          )}
          <button
            type="button"
            className="btn-primary"
            onClick={onGenerate}
            disabled={busy}
          >
            {busy ? 'Generating…' : 'Generate pair code'}
          </button>
        </div>
      )}

      {err && <p className="pair-error">{err}</p>}
    </section>
  )
}

function formatSeconds(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${s.toString().padStart(2, '0')}`
}
