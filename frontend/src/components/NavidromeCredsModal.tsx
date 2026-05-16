import { useEffect, useRef, useState } from 'react'
import HCaptcha from '@hcaptcha/react-hcaptcha'
import {
  getNavidromeCreds,
  rotateNavidromePassword,
  type NavidromeCreds,
  type RotateResult,
} from '../lib/api'
import { getConfig } from '../lib/supabase'

type Mode = 'view' | 'rotate'

interface Props {
  mode: Mode
  onClose: () => void
}

// NavidromeCredsModal gates access to Navidrome admin credentials
// behind a Supabase password re-prompt. The pattern matches the
// banking-app norm of asking for the password again before showing
// or rotating high-impact secrets, even inside an authenticated
// session — a stale tab on an unattended laptop shouldn't be enough.
//
// State machine:
//   prompt  → user types password
//   loading → reauthenticating + calling protected endpoint
//   result  → showing the credentials (or new password for rotate)
//   error   → bad password or network failure; user can retry
export default function NavidromeCredsModal({ mode, onClose }: Props) {
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [creds, setCreds] = useState<NavidromeCreds | null>(null)
  const [rotated, setRotated] = useState<RotateResult | null>(null)
  const [revealPassword, setRevealPassword] = useState(false)
  // hCaptcha state — mirrors Login.tsx's pattern. Required when the
  // Supabase project enforces captcha (prod). Token is single-use and
  // gets reset after any submission so a retry is forced to solve a
  // fresh challenge — preserves the brute-force resistance captcha is
  // there to provide.
  const [captchaToken, setCaptchaToken] = useState<string | null>(null)
  const captchaRef = useRef<HCaptcha | null>(null)

  // Dev mode skips the re-auth prompt — the server doesn't enforce
  // the iat freshness check when BRIDGE_DEV=true, so requiring the
  // developer to re-type their test password every time adds friction
  // without adding security. The dev banner makes the difference from
  // production visible at a glance.
  let isDev = false
  let captchaSiteKey = ''
  try {
    const cfg = getConfig()
    isDev = !!cfg.dev_mode
    captchaSiteKey = cfg.hcaptcha_site_key || ''
  } catch {
    isDev = false
  }
  // Captcha is shown only when the project requires it AND we'd be
  // calling signInWithPassword (i.e. not the dev shortcut path).
  const captchaRequired = !!captchaSiteKey && !isDev

  function resetCaptcha() {
    captchaRef.current?.resetCaptcha()
    setCaptchaToken(null)
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  // In dev + view mode, auto-fetch on open. Rotate stays click-to-
  // confirm even in dev because it's destructive — we want the
  // explicit action either way.
  useEffect(() => {
    if (!isDev || mode !== 'view') return
    let cancelled = false
    ;(async () => {
      setSubmitting(true)
      try {
        const c = await getNavidromeCreds()
        if (!cancelled) setCreds(c)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to fetch')
      } finally {
        if (!cancelled) setSubmitting(false)
      }
    })()
    return () => { cancelled = true }
  }, [isDev, mode])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (submitting) return
    // In prod, password is required. In dev (skipped prompt), the
    // rotate flow gets here via the confirm button with no password.
    if (!isDev && !password) return
    if (captchaRequired && !captchaToken) {
      setError('Please complete the captcha verification.')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      const pw = isDev ? undefined : password
      const ct = isDev ? undefined : (captchaToken ?? undefined)
      if (mode === 'view') {
        const c = await getNavidromeCreds(pw, ct)
        setCreds(c)
      } else {
        const r = await rotateNavidromePassword(pw, ct)
        setRotated(r)
      }
      // Clear the password from React state immediately after use.
      // Reduces the window during which it sits in memory; the form
      // input is replaced by the result view below.
      setPassword('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Authentication failed')
      // Captcha tokens are single-use; reset so the user is forced to
      // solve a fresh challenge on retry. Matches the Login flow.
      resetCaptcha()
    } finally {
      setSubmitting(false)
    }
  }

  const showingResult = !!creds || !!rotated

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" role="dialog" aria-labelledby="nd-creds-title" onClick={e => e.stopPropagation()}>
        <header className="modal-header">
          <h2 id="nd-creds-title">
            {mode === 'view' ? 'Navidrome credentials' : 'Rotate Navidrome password'}
          </h2>
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
        </header>

        {!showingResult && (
          <>
            {isDev && (
              <div className="modal-warning" style={{ background: 'rgba(245, 158, 11, 0.08)', borderColor: 'rgba(245, 158, 11, 0.3)', color: '#f59e0b' }}>
                <strong>Dev mode:</strong> re-auth prompt skipped. In production you'd be required
                to re-enter your password before {mode === 'view' ? 'viewing' : 'rotating'} these credentials.
              </div>
            )}

            {!isDev && (
              <div className="modal-warning">
                {mode === 'view'
                  ? 'Re-enter your password to view the Navidrome admin credentials. This account can modify your entire library.'
                  : 'Re-enter your password to rotate the Navidrome admin password. Any other app using the old password will need to be updated.'}
              </div>
            )}

            {/* In dev + view, the useEffect above auto-fetches and
                we'll already be in `submitting` or showing creds —
                no form needed. In dev + rotate, we still need an
                explicit confirm button. In prod, full password form. */}
            {(!isDev || mode === 'rotate') && (
              <form onSubmit={handleSubmit} className="modal-form">
                {!isDev && (
                  <label>
                    <span>Your account password</span>
                    <input
                      type="password"
                      value={password}
                      onChange={e => setPassword(e.target.value)}
                      autoFocus
                      autoComplete="current-password"
                      disabled={submitting}
                    />
                  </label>
                )}

                {captchaRequired && (
                  <div style={{ display: 'flex', justifyContent: 'center', minHeight: 78 }}>
                    <HCaptcha
                      ref={captchaRef}
                      sitekey={captchaSiteKey}
                      onVerify={(token) => setCaptchaToken(token)}
                      onExpire={() => setCaptchaToken(null)}
                      onError={() => setCaptchaToken(null)}
                      theme="dark"
                    />
                  </div>
                )}

                {error && <div className="modal-error">{error}</div>}

                <footer className="modal-actions">
                  <button type="button" className="btn-secondary" onClick={onClose} disabled={submitting}>
                    Cancel
                  </button>
                  <button
                    type="submit"
                    className="btn-primary"
                    disabled={
                      (!isDev && !password) ||
                      (captchaRequired && !captchaToken) ||
                      submitting
                    }
                  >
                    {submitting
                      ? (mode === 'view' ? 'Loading…' : 'Rotating…')
                      : mode === 'view' ? 'Show credentials' : 'Rotate password'}
                  </button>
                </footer>
              </form>
            )}

            {isDev && mode === 'view' && submitting && (
              <div className="modal-form" style={{ alignItems: 'center', padding: '2rem 1.25rem' }}>
                <span className="spinner-sm" /> Loading credentials…
              </div>
            )}
            {isDev && mode === 'view' && error && (
              <div className="modal-form">
                <div className="modal-error">{error}</div>
                <footer className="modal-actions">
                  <button type="button" className="btn-primary" onClick={onClose}>Close</button>
                </footer>
              </div>
            )}
          </>
        )}

        {creds && <CredentialsView creds={creds} reveal={revealPassword} setReveal={setRevealPassword} onClose={onClose} />}
        {rotated && <RotatedView result={rotated} onClose={onClose} />}
      </div>
    </div>
  )
}

function CredentialsView({
  creds,
  reveal,
  setReveal,
  onClose,
}: {
  creds: NavidromeCreds
  reveal: boolean
  setReveal: (v: boolean) => void
  onClose: () => void
}) {
  return (
    <div className="modal-form">
      <CredField label="Username" value={creds.username} />
      <CredField label="Password" value={creds.password} mask={!reveal} onToggleMask={() => setReveal(!reveal)} />
      <div className="cred-links">
        <a href={creds.navidrome_url} target="_blank" rel="noopener noreferrer" className="btn-secondary">
          Open Navidrome
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" /><polyline points="15 3 21 3 21 9" /><line x1="10" y1="14" x2="21" y2="3" /></svg>
        </a>
      </div>
      <div className="cred-help">
        Use these to sign into Navidrome's UI directly — for example, to trigger a full library scan.
        Treat this password like a master key; never commit it to source control or share it in plaintext.
      </div>
      <footer className="modal-actions">
        <button type="button" className="btn-primary" onClick={onClose}>Done</button>
      </footer>
    </div>
  )
}

function RotatedView({ result, onClose }: { result: RotateResult; onClose: () => void }) {
  return (
    <div className="modal-form">
      <div className="modal-warning" style={{ background: 'rgba(34, 197, 94, 0.08)', borderColor: 'rgba(34, 197, 94, 0.3)', color: '#22c55e' }}>
        Password rotated. Copy it now — for security reasons we won't show it again. You can always rotate again if needed.
      </div>
      <CredField label="Username" value={result.username} />
      <CredField label="New password" value={result.password} mask={false} />
      <footer className="modal-actions">
        <button type="button" className="btn-primary" onClick={onClose}>I've saved it</button>
      </footer>
    </div>
  )
}

function CredField({
  label,
  value,
  mask,
  onToggleMask,
}: {
  label: string
  value: string
  mask?: boolean
  onToggleMask?: () => void
}) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard write can fail in non-secure contexts; user can
      // still select+copy manually from the visible field.
    }
  }

  return (
    <div className="cred-field">
      <span className="cred-label">{label}</span>
      <div className="cred-value-row">
        <code className="cred-value">{mask ? '•'.repeat(Math.min((value ?? '').length, 24)) : (value ?? '')}</code>
        {onToggleMask && (
          <button type="button" className="cred-action" onClick={onToggleMask} title={mask ? 'Show' : 'Hide'}>
            {mask ? 'Show' : 'Hide'}
          </button>
        )}
        <button type="button" className="cred-action" onClick={copy} title="Copy">
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  )
}
