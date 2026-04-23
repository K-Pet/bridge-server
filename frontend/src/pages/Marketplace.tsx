import { useCallback, useEffect, useRef, useState } from 'react'
import { getConfig, getSupabase } from '../lib/supabase'

// Marketplace tab — embeds the Bridge Music Marketplace (Expo web bundle)
// in an iframe so musicians can browse + buy without leaving the home server.
// The surrounding shell (sidebar / player) is preserved; the storefront owns
// the frame content.
//
// Session handoff protocol:
//   1. Bridge-server renders iframe → marketplace Expo app boots
//   2. Marketplace's AuthProvider mounts, sets up its message listener,
//      then sends { type: 'bridge.ready' } to the parent window
//   3. We receive 'bridge.ready' and respond with { type: 'bridge.session',
//      accessToken, refreshToken }
//   4. Marketplace calls supabase.auth.setSession() → user is logged in
//
// This handshake avoids the race where postMessage fires before the
// iframe's React tree has mounted its listener.

export default function Marketplace() {
  const cfg = getConfig()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const marketplaceURL = cfg.marketplace_url || '/marketplace/'

  // Track whether the iframe has signalled readiness so we don't try to
  // postMessage before it has navigated to the marketplace origin.
  const iframeReady = useRef(false)

  // Send the current Supabase session to the iframe.
  const forwardSession = useCallback(async () => {
    if (!iframeReady.current) {
      console.log('[bridge:marketplace] forwardSession skipped — iframe not ready')
      return
    }
    const supabase = getSupabase()
    if (!supabase) return
    const { data } = await supabase.auth.getSession()
    const frame = iframeRef.current?.contentWindow
    if (!frame || !data.session) return
    console.log('[bridge:marketplace] forwarding session to iframe, target:', targetOrigin(marketplaceURL))
    try {
      // Send both tokens — the marketplace needs the refresh token for
      // supabase.auth.setSession(). In production the iframe is same-origin
      // (served at /marketplace/), so this is safe. The access token is
      // short-lived; the refresh token lets the iframe maintain its session
      // across navigations without re-handshaking.
      frame.postMessage(
        {
          type: 'bridge.session',
          accessToken: data.session.access_token,
          refreshToken: data.session.refresh_token,
        },
        targetOrigin(marketplaceURL),
      )
    } catch (err) {
      console.warn('failed to forward session to marketplace iframe', err)
    }
  }, [marketplaceURL])

  // Listen for 'bridge.ready' from the iframe — the marketplace sends this
  // once its AuthProvider has mounted and registered its message listener.
  // Also keep forwarding on auth state changes for token refreshes.
  useEffect(() => {
    const handleMessage = (ev: MessageEvent) => {
      console.log('[bridge:marketplace] message received:', ev.data?.type, 'origin:', ev.origin)
      if (ev.data?.type === 'bridge.ready') {
        iframeReady.current = true
        forwardSession()
      }
    }
    window.addEventListener('message', handleMessage)

    const supabase = getSupabase()
    let unsubAuth: (() => void) | undefined
    if (supabase) {
      const { data: { subscription } } = supabase.auth.onAuthStateChange(() => {
        forwardSession()
      })
      unsubAuth = () => subscription.unsubscribe()
    }

    return () => {
      window.removeEventListener('message', handleMessage)
      unsubAuth?.()
    }
  }, [forwardSession])

  return (
    <div className="marketplace-embed">
      <header className="marketplace-embed-header">
        <div className="marketplace-embed-title">
          <span className="marketplace-embed-eyebrow">Store</span>
          <h1>Bridge Marketplace</h1>
        </div>
        <a
          href={marketplaceURL}
          target="_blank"
          rel="noopener noreferrer"
          className="btn-secondary btn-sm"
          title="Open the storefront in a new tab"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
            <polyline points="15 3 21 3 21 9" />
            <line x1="10" y1="14" x2="21" y2="3" />
          </svg>
          Open in new tab
        </a>
      </header>

      <div className="marketplace-embed-frame">
        {loading && !error && (
          <div className="marketplace-embed-loader">
            <div className="spinner" />
            <p>Loading storefront…</p>
          </div>
        )}
        {error && (
          <div className="marketplace-embed-error">
            <strong>Can't reach the storefront.</strong>
            <p>{error}</p>
            <p className="marketplace-embed-hint">
              Start the marketplace dev server: <code>cd Bridge-Music-Marketplace && tilt up</code>
            </p>
          </div>
        )}
        <iframe
          ref={iframeRef}
          src={marketplaceURL}
          title="Bridge Music Marketplace"
          className={`marketplace-embed-iframe ${loading ? 'is-loading' : ''}`}
          // Allow Stripe redirects, Supabase auth popups, and payment request APIs.
          allow="payment; clipboard-read; clipboard-write"
          // sandbox intentionally omitted — we trust the same-project storefront
          // and it needs full cookie/storage access for Supabase auth.
          onLoad={() => setLoading(false)}
          onError={() => setError('Network error loading storefront.')}
        />
      </div>
    </div>
  )
}

function targetOrigin(url: string): string {
  try {
    // Relative URLs (same-origin mount) — use the current origin.
    if (url.startsWith('/')) return window.location.origin
    return new URL(url).origin
  } catch {
    return window.location.origin
  }
}
