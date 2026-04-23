import { useState } from 'react'
import { getSupabase } from '../lib/supabase'

type Mode = 'login' | 'signup'

export default function Login() {
  const [mode, setMode] = useState<Mode>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [signupDone, setSignupDone] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    setLoading(true)

    const supabase = getSupabase()
    if (!supabase) {
      setError('Authentication is not configured')
      setLoading(false)
      return
    }

    if (mode === 'login') {
      const { error } = await supabase.auth.signInWithPassword({ email, password })
      if (error) setError(error.message)
    } else {
      const { error } = await supabase.auth.signUp({ email, password })
      if (error) {
        setError(error.message)
      } else {
        setSignupDone(true)
      }
    }
    setLoading(false)
  }

  function switchMode(next: Mode) {
    setMode(next)
    setError('')
    setSignupDone(false)
  }

  if (signupDone) {
    return (
      <div className="login-page">
        <div className="login-card">
          <h1>Check your email</h1>
          <p>
            We sent a confirmation link to <strong>{email}</strong>.
            Click it to activate your account, then come back here to sign in.
          </p>
          <button
            type="button"
            className="auth-switch-btn"
            onClick={() => switchMode('login')}
          >
            Back to Sign In
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="login-page">
      <div className="login-card">
        <h1>Bridge Music</h1>
        <p>{mode === 'login' ? 'Sign in to your music server' : 'Create your account'}</p>

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

        <form onSubmit={handleSubmit}>
          <input
            type="email"
            placeholder="Email"
            value={email}
            onChange={e => setEmail(e.target.value)}
            required
          />
          <input
            type="password"
            placeholder="Password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            required
            minLength={mode === 'signup' ? 6 : undefined}
          />
          {error && <div className="error">{error}</div>}
          <button type="submit" disabled={loading}>
            {loading
              ? (mode === 'login' ? 'Signing in...' : 'Creating account...')
              : (mode === 'login' ? 'Sign In' : 'Create Account')
            }
          </button>
        </form>
      </div>
    </div>
  )
}
