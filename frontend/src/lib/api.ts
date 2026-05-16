import { getSupabase } from './supabase'
import { bumpLibraryVersion } from './library-version'

async function authHeaders(): Promise<Record<string, string>> {
  const supabase = getSupabase()
  if (!supabase) return {}
  const { data } = await supabase.auth.getSession()
  const token = data.session?.access_token
  if (!token) return {}
  return { Authorization: `Bearer ${token}` }
}

export async function apiFetch<T>(path: string, init?: RequestInit & { token?: string }): Promise<T> {
  // Allow callers to pass an explicit token (avoids getSession() race after sign-in)
  let hdrs: Record<string, string>
  if (init?.token) {
    hdrs = { Authorization: `Bearer ${init.token}` }
  } else {
    hdrs = await authHeaders()
  }
  const headers = { ...hdrs, ...init?.headers }
  const res = await fetch(path, { ...init, headers })
  if (!res.ok) {
    throw new Error(`API ${res.status}: ${await res.text()}`)
  }
  return res.json()
}

export async function getHealth() {
  return apiFetch<{ status: string }>('/api/health')
}

export interface PurchasedTrack {
  id: string
  title: string
  artist: string
  format: string | null
  disc_number: number | null
  album_index: number | null
}

export interface PurchaseItem {
  id: string
  track_id: string | null
  album_id: string | null
  price_cents: number
  track: (PurchasedTrack & { album_id: string | null }) | null
  album: {
    id: string
    title: string
    artist: string
    cover_art_url: string | null
    tracks: PurchasedTrack[]
  } | null
}

export interface DeliverySummary {
  total: number
  by_status?: Record<string, number>
  terminal?: boolean
  all_complete?: boolean
  any_failed?: boolean
  last_error?: string
}

export interface Purchase {
  id: string
  total_cents: number
  status: string
  payment_ref: string | null
  created_at: string
  purchase_items: PurchaseItem[]
  delivery?: DeliverySummary
}

export async function getPurchases() {
  return apiFetch<Purchase[]>('/api/purchases')
}

export async function redeliverPurchase(purchaseId: string) {
  return apiFetch<{ purchase_id: string; status: string; delivery_error: string }>(
    `/api/purchases/${encodeURIComponent(purchaseId)}/redeliver`,
    { method: 'POST' }
  )
}

export async function getTrackDownloadURL(trackId: string) {
  return apiFetch<{ url: string; filename: string }>(
    `/api/tracks/${encodeURIComponent(trackId)}/download`
  )
}

// downloadAlbumZip streams the album zip through the bridge-server (which
// requires our auth header) and triggers a browser download via a blob URL.
// Holds the whole album in memory for the duration of the download — fine for
// typical album sizes; revisit if we need to support multi-GB libraries.
export async function downloadAlbumZip(albumId: string): Promise<void> {
  const headers = await authHeaders()
  const res = await fetch(`/api/albums/${encodeURIComponent(albumId)}/zip`, { headers })
  if (!res.ok) {
    throw new Error(`API ${res.status}: ${await res.text()}`)
  }
  const blob = await res.blob()

  let filename = `album-${albumId}.zip`
  const dispo = res.headers.get('Content-Disposition') || ''
  const match = dispo.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
  if (match?.[1]) filename = decodeURIComponent(match[1])

  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

export async function deleteSong(songId: string) {
  return apiFetch<{ deleted: boolean; song_id: string; scanning: boolean }>(
    `/api/library/songs/${encodeURIComponent(songId)}`,
    { method: 'DELETE' }
  )
}

// SongTagsUpdate uses PATCH semantics — only fields included in the
// request body are written. To leave a field unchanged, omit the key;
// to clear a field, send an empty string (or 0 for numeric fields).
export interface SongTagsUpdate {
  title?: string
  artist?: string
  album_artist?: string
  album?: string
  year?: number
  track_number?: number
  disc_number?: number
  genre?: string
}

export async function updateSongTags(songId: string, tags: SongTagsUpdate) {
  return apiFetch<{ updated: boolean; song_id: string; scanning: boolean }>(
    `/api/library/songs/${encodeURIComponent(songId)}`,
    { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(tags) }
  )
}

// AlbumTagsUpdate covers fields that make sense at album scope. Per-
// track fields (title, track_number) go through updateSongTags.
export interface AlbumTagsUpdate {
  album_artist?: string
  album?: string
  year?: number
  genre?: string
}

export async function updateAlbumTags(albumId: string, tags: AlbumTagsUpdate) {
  return apiFetch<{
    updated: boolean
    album_id: string
    updated_ids: string[]
    failed_ids: string[]
    scanning: boolean
  }>(
    `/api/library/albums/${encodeURIComponent(albumId)}`,
    { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(tags) }
  )
}

// RenameArtistResult mirrors the server response. The server does a
// smart cascade: AlbumArtist is rewritten universally, but Track
// Artist is rewritten only on tracks where it exactly matches the old
// artist name (no features). The counts let the UI surface "Renamed
// 47 tracks; left 8 features intact."
export interface RenameArtistResult {
  renamed: boolean
  artist_id: string
  old_name: string
  new_name: string
  renamed_track_count: number
  feature_preserved_count: number
  failed_count: number
  scanning: boolean
}

export async function renameArtist(artistId: string, newName: string) {
  return apiFetch<RenameArtistResult>(
    `/api/library/artists/${encodeURIComponent(artistId)}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ new_name: newName }),
    }
  )
}

// IdentifyCandidate mirrors autotag.Candidate on the Go side. Score
// is AcoustID confidence (0–1); track/disc/year are optional because
// MusicBrainz doesn't always have release-level metadata for a
// recording.
export interface IdentifyCandidate {
  score: number
  recording_id: string
  title: string
  artist: string
  album_artist?: string
  album?: string
  year?: number
  track_number?: number
  disc_number?: number
  musicbrainz_url?: string
}

export async function identifySong(songId: string) {
  return apiFetch<{ song_id: string; candidates: IdentifyCandidate[] }>(
    `/api/library/songs/${encodeURIComponent(songId)}/identify`,
    { method: 'POST' }
  )
}

// uploadArtistPhoto replaces the folder-level artist photo. Same
// raw-PUT shape as uploadAlbumCover but targets the artist folder.
//
// Bumps the library-version cache buster on success so any post-
// upload render (including the immediate refresh()) generates fresh
// cover-art URLs. Without this the SSE library_updated event would
// arrive after the first re-render, leaving a render-cycle window
// in which the <img> URL still has the pre-upload nonce and the
// browser serves a cached old image.
export async function uploadArtistPhoto(artistId: string, file: File) {
  const hdrs = await authHeaders()
  const res = await fetch(`/api/library/artists/${encodeURIComponent(artistId)}/photo`, {
    method: 'PUT',
    headers: { ...hdrs, 'Content-Type': file.type },
    body: file,
  })
  if (!res.ok) {
    throw new Error(`API ${res.status}: ${await res.text()}`)
  }
  const result = await res.json() as { updated: boolean; artist_id: string; bytes: number; scanning: boolean }
  bumpLibraryVersion()
  return result
}

// uploadAlbumCover replaces the album's folder-level cover image.
// The server expects a raw PUT body (no multipart wrapper) with a
// Content-Type of image/jpeg or image/png. File.type is exactly the
// MIME the browser inferred from the selected file's extension, so
// passing it through is safe — the server rejects anything else.
//
// Uses fetch() directly rather than apiFetch because we send a binary
// body with a non-JSON Content-Type; apiFetch is JSON-shaped.
export async function uploadAlbumCover(albumId: string, file: File) {
  const hdrs = await authHeaders()
  const res = await fetch(`/api/library/albums/${encodeURIComponent(albumId)}/cover`, {
    method: 'PUT',
    headers: { ...hdrs, 'Content-Type': file.type },
    body: file,
  })
  if (!res.ok) {
    throw new Error(`API ${res.status}: ${await res.text()}`)
  }
  const result = await res.json() as { updated: boolean; album_id: string; bytes: number; scanning: boolean }
  bumpLibraryVersion()
  return result
}

export async function deleteAlbum(albumId: string) {
  return apiFetch<{ deleted: boolean; album_id: string; song_count: number; scanning: boolean }>(
    `/api/library/albums/${encodeURIComponent(albumId)}`,
    { method: 'DELETE' }
  )
}

export interface Entitlements {
  album_ids: string[]
  track_ids: string[]
}

export async function getEntitlements() {
  return apiFetch<Entitlements>('/api/entitlements')
}

export async function getSettings() {
  return apiFetch<{ delivery_mode: string; poll_interval: string }>('/api/settings')
}

// ── Navidrome admin credentials ─────────────────────────────────────
// Reveal/rotate flows for the Navidrome admin account that bridge
// bootstrapped on first run. Both require a freshly-issued Supabase
// JWT (iat within 5 min) so a stale tab can't expose the underlying
// library-admin password. The frontend reauthenticates via
// supabase.auth.signInWithPassword, which mints a fresh token, then
// calls the protected endpoint with that token.

export interface NavidromeCreds {
  username: string
  password: string
  navidrome_url: string
  proxy_path: string
}

export interface RotateResult {
  rotated: boolean
  username: string
  password: string
}

// reauthenticateAndGetToken signs the current user back in with their
// password to mint a fresh JWT. Returns the new access token. The
// password never touches bridge-server — it goes directly to Supabase
// over HTTPS.
async function reauthenticateAndGetToken(password: string): Promise<string> {
  const supabase = getSupabase()
  if (!supabase) throw new Error('Supabase not configured')
  const { data: userData } = await supabase.auth.getUser()
  const email = userData.user?.email
  if (!email) throw new Error('No active session')
  const { data, error } = await supabase.auth.signInWithPassword({ email, password })
  if (error) throw new Error(error.message)
  const token = data.session?.access_token
  if (!token) throw new Error('Re-authentication did not return a session')
  return token
}

// Password is optional. In dev mode (where the server skips the
// iat-freshness check) we call the endpoint with the existing
// session token — saves the developer from re-typing their test
// password to exercise the flow. Production callers always pass it.
export async function getNavidromeCreds(password?: string): Promise<NavidromeCreds> {
  const init = password !== undefined
    ? { token: await reauthenticateAndGetToken(password) }
    : undefined
  return apiFetch<NavidromeCreds>('/api/settings/navidrome-creds', init)
}

export async function rotateNavidromePassword(password?: string): Promise<RotateResult> {
  const init = password !== undefined
    ? { method: 'POST' as const, token: await reauthenticateAndGetToken(password) }
    : { method: 'POST' as const }
  return apiFetch<RotateResult>('/api/settings/navidrome-creds/rotate', init)
}

export interface PairCode {
  code: string
  expires_at: string
  ttl_sec: number
}

export async function generatePairCode() {
  return apiFetch<PairCode>('/api/pair/generate', { method: 'POST' })
}

// ── Onboarding ──────────────────────────────────────────────────────

export interface OnboardingStatus {
  profile_complete: boolean
  server_paired: boolean
  auto_pair_available: boolean
  profile: { id: string; username: string | null; full_name: string | null; avatar_url: string | null } | null
  server: { id: string; label: string; server_id: string | null; webhook_url: string } | null
}

export async function getOnboardingStatus(token?: string) {
  return apiFetch<OnboardingStatus>('/api/onboarding/status', token ? { token } : undefined)
}

export interface AutoPairResult {
  paired: boolean
  server: { id: string; label: string; server_id: string | null; webhook_url: string }
}

export async function autoPair(token?: string) {
  return apiFetch<AutoPairResult>('/api/auto-pair', { method: 'POST', token })
}

export async function getPairStatus() {
  return apiFetch<{ paired: boolean; server: any }>('/api/pair/status')
}

// ── Events ──────────────────────────────────────────────────────────

export interface BridgeEvent {
  type: string
  purchase_id?: string
  task_id?: string
  status?: string
  data?: Record<string, unknown>
}

// subscribeEvents opens an SSE connection to the bridge server and invokes
// onEvent for every event. Returns an unsubscribe function.
// EventSource handles reconnection automatically on transient drops.
//
// EventSource cannot set custom headers, so we pass the JWT via a query
// parameter. The token is short-lived and transmitted over the same origin,
// so this is safe for same-origin SSE (the URL is never logged or shared).
export async function subscribeEvents(onEvent: (e: BridgeEvent) => void): Promise<() => void> {
  let url = '/api/events'
  const supabase = getSupabase()
  if (supabase) {
    const { data } = await supabase.auth.getSession()
    const token = data.session?.access_token
    if (token) {
      url += `?token=${encodeURIComponent(token)}`
    }
  }
  const es = new EventSource(url)
  const handler = (ev: MessageEvent) => {
    try {
      const parsed = JSON.parse(ev.data) as BridgeEvent
      onEvent(parsed)
    } catch {
      // Ignore malformed payloads; heartbeats come through as SSE comments
      // and never fire `message`, so this should only trip on server bugs.
    }
  }
  // Listen for every event type the server emits today.
  for (const t of ['hello', 'task_status', 'library_updated', 'purchase_enqueued']) {
    es.addEventListener(t, handler)
  }
  return () => es.close()
}

