import { useEffect, useRef, useState } from 'react'
import { getConfig, getSupabase } from '../lib/supabase'

// Marketplace tab — embeds the Bridge Music Marketplace (Expo web bundle)
// in an iframe so musicians can browse + buy without leaving the home server.
// The surrounding shell (sidebar / player) is preserved; the storefront owns
// the frame content.
//
// Session handoff: when the iframe loads, we forward the current Supabase
// access + refresh tokens via postMessage. The storefront reads the message
// on boot, rehydrates its own Supabase client, and the user lands already
// signed in. In dev the two bundles share the same Supabase project, so
// purchases completed in the iframe flow back to the home server via the
// existing Stripe → deliver-purchase → /api/webhook/purchase pipeline.

export default function Marketplace() {
  const cfg = getConfig()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const marketplaceURL = cfg.marketplace_url || '/marketplace/'

  useEffect(() => {
    const supabase = getSupabase()
    if (!supabase || !iframeRef.current) return

    // Push the session into the frame on every auth change so token refreshes
    // stay in sync. The storefront validates origin before accepting.
    const forward = async () => {
      const { data } = await supabase.auth.getSession()
      const frame = iframeRef.current?.contentWindow
      if (!frame || !data.session) return
      try {
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
    }

    forward()
    const { data: { subscription } } = supabase.auth.onAuthStateChange(forward)
    return () => subscription.unsubscribe()
  }, [marketplaceURL])

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
