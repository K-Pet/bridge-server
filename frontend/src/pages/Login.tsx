import { useEffect, useRef, useState } from 'react'
import HCaptcha from '@hcaptcha/react-hcaptcha'
import { getConfig, getSupabase } from '../lib/supabase'

type Mode = 'login' | 'signup' | 'forgot' | 'recovery'

// The Supabase project requires 6+ chars (config.toml: minimum_password_length = 6).
// We bump the UX floor to 8 because that's the password length Supabase
// itself recommends in the same comment, and matches OWASP's "memorized
// secret" baseline. The actual enforcement still happens server-side.
const PASSWORD_MIN_LENGTH = 8

// Plain-language messages for the auth error codes we expect to surface
// to end users. Anything not in this map falls back to the raw message.
function friendlyAuthError(err: { message?: string; code?: string; status?: number } | null): string {
  if (!err) return ''
  switch (err.code) {
    case 'invalid_credentials':
      return 'Email or password is incorrect.'
    case 'email_not_confirmed':
      return 'Please confirm your email address — check your inbox for the link we sent.'
    case 'user_already_exists':
    case 'email_exists':
      return 'An account with this email already exists. Try signing in instead.'
    case 'weak_password':
      return err.message || `Password is too weak. Use at least ${PASSWORD_MIN_LENGTH} characters.`
    case 'over_email_send_rate_limit':
    case 'over_request_rate_limit':
      return 'Too many attempts. Please wait a minute and try again.'
    case 'captcha_failed':
      return 'Captcha verification failed. Please try again.'
    case 'signup_disabled':
      return 'New sign-ups are temporarily disabled.'
    case 'validation_failed':
      return err.message || 'Please check your input and try again.'
  }
  if (err.status === 429) return 'Too many attempts. Please wait a minute and try again.'
  return err.message || 'Something went wrong. Please try again.'
}

function isValidEmail(email: string): boolean {
  // Pragmatic email check — the Supabase server is authoritative.
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())
}

export default function Login() {
  const cfg = getConfig()
  const captchaSiteKey = cfg.hcaptcha_site_key
  const captchaRequired = !!captchaSiteKey

  const [mode, setMode] = useState<Mode>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [info, setInfo] = useState('')
  const [loading, setLoading] = useState(false)
  const [captchaToken, setCaptchaToken] = useState<string | null>(null)
  const captchaRef = useRef<HCaptcha | null>(null)

  // Detect the password recovery flow: when the user clicks the reset
  // link in their email, Supabase puts the recovery session on the
  // client and emits a PASSWORD_RECOVERY event. We switch the form to
  // "set new password" mode rather than dropping them into the app.
  useEffect(() => {
    const supabase = getSupabase()
    if (!supabase) return
    const { data: { subscription } } = supabase.auth.onAuthStateChange((event) => {
      if (event === 'PASSWORD_RECOVERY') {
        setMode('recovery')
        setError('')
        setInfo('')
      }
    })
    return () => subscription.unsubscribe()
  }, [])

  function resetCaptcha() {
    captchaRef.current?.resetCaptcha()
    setCaptchaToken(null)
  }

  function switchMode(next: Mode) {
    setMode(next)
    setError('')
    setInfo('')
    setPassword('')
    setConfirmPassword('')
    resetCaptcha()
  }

  function validateForLogin(): string | null {
    if (!isValidEmail(email)) return 'Please enter a valid email address.'
    if (!password) return 'Please enter your password.'
    return null
  }

  function validateForSignup(): string | null {
    if (!isValidEmail(email)) return 'Please enter a valid email address.'
    if (password.length < PASSWORD_MIN_LENGTH) {
      return `Password must be at least ${PASSWORD_MIN_LENGTH} characters.`
    }
    if (password !== confirmPassword) return 'Passwords do not match.'
    return null
  }

  function validateForRecovery(): string | null {
    if (password.length < PASSWORD_MIN_LENGTH) {
      return `Password must be at least ${PASSWORD_MIN_LENGTH} characters.`
    }
    if (password !== confirmPassword) return 'Passwords do not match.'
    return null
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    setInfo('')

    const supabase = getSupabase()
    if (!supabase) {
      setError('Authentication is not configured.')
      return
    }

    // Pre-validate before kicking off captcha so we don't burn a token
    // on a form that won't pass our own checks.
    const validationError =
      mode === 'login' ? validateForLogin()
      : mode === 'signup' ? validateForSignup()
      : mode === 'forgot' ? (isValidEmail(email) ? null : 'Please enter a valid email address.')
      : validateForRecovery()
    if (validationError) {
      setError(validationError)
      return
    }

    if (captchaRequired && !captchaToken && mode !== 'recovery') {
      setError('Please complete the captcha verification.')
      return
    }

    setLoading(true)
    try {
      if (mode === 'login') {
        const { error } = await supabase.auth.signInWithPassword({
          email: email.trim(),
          password,
          options: { captchaToken: captchaToken ?? undefined },
        })
        if (error) {
          setError(friendlyAuthError(error))
          resetCaptcha()
        }
      } else if (mode === 'signup') {
        const { data, error } = await supabase.auth.signUp({
          email: email.trim(),
          password,
          options: {
            captchaToken: captchaToken ?? undefined,
            // Send the user back to this same page after they click the
            // confirmation link in their email.
            emailRedirectTo: window.location.origin,
          },
        })
        if (error) {
          setError(friendlyAuthError(error))
          resetCaptcha()
        } else if (!data.session) {
          // Email confirmation required — Supabase returns a user with
          // no session until the link is clicked. If a row already
          // existed for this email Supabase returns the existing user
          // silently to prevent enumeration; the message we show is
          // intentionally identical either way.
          setInfo(
            `If an account doesn't exist for ${email.trim()}, we've sent a confirmation link. Click it to activate your account, then come back to sign in.`
          )
          resetCaptcha()
        }
        // If session is non-null, App.tsx's onAuthStateChange picks it
        // up and routes us into onboarding.
      } else if (mode === 'forgot') {
        const { error } = await supabase.auth.resetPasswordForEmail(email.trim(), {
          captchaToken: captchaToken ?? undefined,
          // The link in the email lands the user back on /login with
          // a recovery session — our PASSWORD_RECOVERY listener flips
          // the form into "set new password" mode.
          redirectTo: `${window.location.origin}/login`,
        })
        if (error) {
          setError(friendlyAuthError(error))
        } else {
          // Same wording regardless of whether the email exists, to
          // avoid disclosing which addresses are registered.
          setInfo(
            `If an account exists for ${email.trim()}, we've sent a password reset link.`
          )
        }
        resetCaptcha()
      } else if (mode === 'recovery') {
        // The user got here via the reset email — they have a recovery
        // session, so updateUser() with the new password is allowed.
        const { error } = await supabase.auth.updateUser({ password })
        if (error) {
          setError(friendlyAuthError(error))
        } else {
          setInfo('Password updated. You can now use it to sign in.')
          // Sign the recovery session out so the user signs in fresh
          // with the new password — keeps the auth state clean.
          await supabase.auth.signOut()
          setTimeout(() => switchMode('login'), 1500)
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Something went wrong.')
      resetCaptcha()
    } finally {
      setLoading(false)
    }
  }

  // ─── Render helpers ────────────────────────────────────────────────

  const isAuthMode = mode === 'login' || mode === 'signup'
  const showEmailField = mode !== 'recovery'
  const showPasswordField = mode === 'login' || mode === 'signup' || mode === 'recovery'
  const showConfirmPasswordField = mode === 'signup' || mode === 'recovery'
  const showCaptcha = captchaRequired && mode !== 'recovery'

  const submitLabel = (() => {
    if (loading) {
      switch (mode) {
        case 'login': return 'Signing in...'
        case 'signup': return 'Creating account...'
        case 'forgot': return 'Sending link...'
        case 'recovery': return 'Updating password...'
      }
    }
    switch (mode) {
      case 'login': return 'Sign In'
      case 'signup': return 'Create Account'
      case 'forgot': return 'Send Reset Link'
      case 'recovery': return 'Set New Password'
    }
  })()

  const heading = mode === 'forgot' ? 'Reset your password'
    : mode === 'recovery' ? 'Choose a new password'
    : 'Bridge Music'

  const subheading = mode === 'login' ? 'Sign in to your music server'
    : mode === 'signup' ? 'Create your account'
    : mode === 'forgot' ? "Enter your email and we'll send you a reset link."
    : 'Enter a new password for your account.'

  return (
    <div className="login-page">
      <div className="login-card">
        <h1>{heading}</h1>
        <p>{subheading}</p>

        {isAuthMode && (
          <div className="auth-tabs">
            <button
              type="button"
              className={`auth-tab ${mode === 'login' ? 'active' : ''}`}
              onClick={() => switchMode('login')}
            >
              Sign In
            </button>
            <button
              type="button"
              className={`auth-tab ${mode === 'signup' ? 'active' : ''}`}
              onClick={() => switchMode('signup')}
            >
              Sign Up
            </button>
          </div>
        )}

        <form onSubmit={handleSubmit} noValidate>
          {showEmailField && (
            <input
              type="email"
              autoComplete="email"
              placeholder="Email"
              value={email}
              onChange={e => setEmail(e.target.value)}
              required
              disabled={loading}
            />
          )}

          {showPasswordField && (
            <div style={{ position: 'relative' }}>
              <input
                type={showPassword ? 'text' : 'password'}
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                placeholder={mode === 'recovery' ? 'New password' : 'Password'}
                value={password}
                onChange={e => setPassword(e.target.value)}
                required
                minLength={mode === 'login' ? undefined : PASSWORD_MIN_LENGTH}
                disabled={loading}
                style={{ width: '100%', paddingRight: '3.5rem', boxSizing: 'border-box' }}
              />
              <button
                type="button"
                onClick={() => setShowPassword(s => !s)}
                aria-label={showPassword ? 'Hide password' : 'Show password'}
                style={{
                  position: 'absolute',
                  right: '0.5rem',
                  top: '50%',
                  transform: 'translateY(-50%)',
                  background: 'none',
                  border: 'none',
                  color: 'var(--text-secondary)',
                  cursor: 'pointer',
                  fontSize: '0.75rem',
                  padding: '0.25rem 0.5rem',
                }}
                tabIndex={-1}
              >
                {showPassword ? 'Hide' : 'Show'}
              </button>
            </div>
          )}

          {showConfirmPasswordField && (
            <input
              type={showPassword ? 'text' : 'password'}
              autoComplete="new-password"
              placeholder="Confirm password"
              value={confirmPassword}
              onChange={e => setConfirmPassword(e.target.value)}
              required
              minLength={PASSWORD_MIN_LENGTH}
              disabled={loading}
            />
          )}

          {(mode === 'signup' || mode === 'recovery') && password.length > 0 && (
            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
              {password.length < PASSWORD_MIN_LENGTH
                ? `${PASSWORD_MIN_LENGTH - password.length} more character${PASSWORD_MIN_LENGTH - password.length === 1 ? '' : 's'} required`
                : 'Password length looks good.'}
            </div>
          )}

          {showCaptcha && (
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

          {error && <div className="error" role="alert">{error}</div>}
          {info && (
            <div
              role="status"
              style={{
                color: 'var(--text)',
                background: 'var(--accent-soft)',
                border: '1px solid var(--border)',
                padding: '0.6rem 0.75rem',
                borderRadius: 6,
                fontSize: '0.85rem',
                lineHeight: 1.4,
              }}
            >
              {info}
            </div>
          )}

          <button
            type="submit"
            disabled={loading || (showCaptcha && !captchaToken)}
          >
            {submitLabel}
          </button>
        </form>

        {mode === 'login' && (
          <button
            type="button"
            onClick={() => switchMode('forgot')}
            style={{
              marginTop: '0.75rem',
              background: 'none',
              border: 'none',
              color: 'var(--text-secondary)',
              fontSize: '0.8rem',
              cursor: 'pointer',
              textDecoration: 'underline',
              padding: 0,
            }}
          >
            Forgot your password?
          </button>
        )}

        {(mode === 'forgot' || mode === 'recovery') && (
          <button
            type="button"
            className="auth-switch-btn"
            onClick={() => switchMode('login')}
          >
            Back to Sign In
          </button>
        )}
      </div>
    </div>
  )
}
