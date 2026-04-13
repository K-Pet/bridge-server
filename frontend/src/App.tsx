import { useEffect, useState } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { initConfig, getSupabase, isDevMode } from './lib/supabase'
import type { Session } from '@supabase/supabase-js'
import { PlayerProvider } from './context/PlayerContext'
import Layout from './components/Layout'
import Login from './pages/Login'
import Library from './pages/Library'
import ArtistDetail from './pages/ArtistDetail'
import AlbumDetail from './pages/AlbumDetail'
import Playlists from './pages/Playlists'
import PlaylistDetail from './pages/PlaylistDetail'
import Marketplace from './pages/Marketplace'
import Purchases from './pages/Purchases'
import Settings from './pages/Settings'

export default function App() {
  const [session, setSession] = useState<Session | null>(null)
  const [loading, setLoading] = useState(true)
  const [configError, setConfigError] = useState('')

  useEffect(() => {
    initConfig()
      .then(() => {
        const supabase = getSupabase()
        if (!supabase) {
          setLoading(false)
          return
        }

        supabase.auth.getSession().then(({ data }) => {
          setSession(data.session)
          setLoading(false)
        }).catch(() => {
          setLoading(false)
        })

        const { data: { subscription } } = supabase.auth.onAuthStateChange((_event, session) => {
          setSession(session)
        })

        return () => subscription.unsubscribe()
      })
      .catch((err) => {
        setConfigError(err instanceof Error ? err.message : 'Failed to load config')
        setLoading(false)
      })
  }, [])

  if (loading) {
    return <div className="loading-screen"><div className="spinner" /><p>Loading Bridge Music...</p></div>
  }

  if (configError) {
    return <div className="error-page">Error: {configError}</div>
  }

  if (!isDevMode() && !session) {
    return <Login />
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
          <Route path="/purchases" element={<Purchases />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </PlayerProvider>
  )
}
