import { useCallback, useEffect, useState } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { initConfig, getConfig, getSupabase, isDevMode } from './lib/supabase'
import { getOnboardingStatus, type OnboardingStatus } from './lib/api'
import type { Session } from '@supabase/supabase-js'
import { PlayerProvider } from './context/PlayerContext'
import Layout from './components/Layout'
import Login from './pages/Login'
import Onboarding from './pages/Onboarding'
import Library from './pages/Library'
import ArtistDetail from './pages/ArtistDetail'
import AlbumDetail from './pages/AlbumDetail'
import Playlists from './pages/Playlists'
import PlaylistDetail from './pages/PlaylistDetail'
import Marketplace from './pages/Marketplace'
import Settings from './pages/Settings'

export default function App() {
  const [session, setSession] = useState<Session | null>(null)
  const [loading, setLoading] = useState(true)
  const [configError, setConfigError] = useState('')

  // undefined = skip onboarding, null = not checked yet, OnboardingStatus = show wizard
  const [onboarding, setOnboarding] = useState<OnboardingStatus | null | undefined>(undefined)

  useEffect(() => {
    let cancelled = false

    async function init() {
      await initConfig()

      const supabase = getSupabase()
      if (!supabase) {
        setLoading(false)
        return
      }

      // In dev mode, auto-sign-in with the seeded test user so we get a
      // real Supabase JWT. This is the same auth path as production — no
      // special "dev-user" bypass.
      const cfg = getConfig()
      console.log('[bridge] init: dev_mode=', cfg.dev_mode, 'dev_email=', cfg.dev_email)
      if (cfg.dev_mode && cfg.dev_email && cfg.dev_password) {
        // Always do a fresh sign-in in dev mode. The seeded test user's
        // UUID changes on every `supabase db reset`, so a cached session
        // (even with the right email) may carry a stale user ID whose
        // profile row no longer exists.
        const { data: signInData, error } = await supabase.auth.signInWithPassword({
          email: cfg.dev_email,
          password: cfg.dev_password,
        })
        console.log('[bridge] dev signIn: error=', error?.message, 'session=', !!signInData?.session)
        if (error) {
          console.warn('Dev auto-sign-in failed:', error.message)
        }
      }

      // At this point, auto-sign-in (if any) has completed. Read session.
      const { data } = await supabase.auth.getSession()
      console.log('[bridge] final session:', !!data.session, 'token:', data.session?.access_token?.slice(0, 20))
      if (cancelled) return
      setSession(data.session)

      // Check onboarding status now. Pass the access token explicitly to
      // avoid a getSession() race — the Supabase client may not have
      // propagated the session internally yet right after signIn.
      if (data.session) {
        try {
          const status = await getOnboardingStatus(data.session.access_token)
          console.log('[bridge] onboarding status:', status)
          if (cancelled) return
          if (status.profile_complete && status.server_paired) {
            setOnboarding(undefined) // fully onboarded
          } else {
            setOnboarding(status) // show wizard
          }
        } catch (err) {
          console.error('[bridge] onboarding check failed:', err)
          if (!cancelled) setOnboarding(undefined) // on error, skip gracefully
        }
      } else {
        console.log('[bridge] no session — skipping onboarding check')
      }

      if (!cancelled) setLoading(false)

      // Keep session in sync on refreshes / sign-out
      const { data: { subscription } } = supabase.auth.onAuthStateChange((_event, newSession) => {
        setSession(newSession)
        // Re-check onboarding if session changes (e.g. sign-out + re-sign-in)
        if (newSession) {
          setOnboarding(null) // trigger re-check
        } else {
          setOnboarding(undefined) // no session, skip
        }
      })

      return () => subscription.unsubscribe()
    }

    init().catch((err) => {
      if (!cancelled) {
        setConfigError(err instanceof Error ? err.message : 'Failed to load config')
        setLoading(false)
      }
    })

    return () => { cancelled = true }
  }, [])

  // Re-check onboarding when onAuthStateChange resets it to null
  useEffect(() => {
    if (onboarding !== null || !session) return
    let cancelled = false
    getOnboardingStatus()
      .then(status => {
        if (cancelled) return
        if (status.profile_complete && status.server_paired) {
          setOnboarding(undefined)
        } else {
          setOnboarding(status)
        }
      })
      .catch(() => { if (!cancelled) setOnboarding(undefined) })
    return () => { cancelled = true }
  }, [onboarding, session])

  const completeOnboarding = useCallback(() => {
    setOnboarding(undefined)
  }, [])

  if (loading) {
    return <div className="loading-screen"><div className="spinner" /><p>Loading Bridge Music...</p></div>
  }

  if (configError) {
    return <div className="error-page">Error: {configError}</div>
  }

  // No session and not dev mode → show login
  if (!session && !isDevMode()) {
    return <Login />
  }

  // Show onboarding wizard if needed (profile or pairing incomplete)
  if (onboarding && session) {
    return (
      <Onboarding
        status={onboarding}
        userId={session.user.id}
        onComplete={completeOnboarding}
      />
    )
  }

  // Still checking onboarding status (re-check after auth state change)
  if (onboarding === null && session) {
    return <div className="loading-screen"><div className="spinner" /><p>Loading Bridge Music...</p></div>
  }

  return (
    <PlayerProvider>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<Library />} />
          <Route path="/artist/:id" element={<ArtistDetail />} />
          <Route path="/album/:id" element={<AlbumDetail />} />
          <Route path="/playlists" element={<Playlists />} />
          <Route path="/playlist/:id" element={<PlaylistDetail />} />
          <Route path="/marketplace" element={<Marketplace />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </PlayerProvider>
  )
}
