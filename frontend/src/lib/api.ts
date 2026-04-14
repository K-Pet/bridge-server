import { getSupabase } from './supabase'

async function authHeaders(): Promise<Record<string, string>> {
  const supabase = getSupabase()
  if (!supabase) return {}
  const { data } = await supabase.auth.getSession()
  const token = data.session?.access_token
  if (!token) return {}
  return { Authorization: `Bearer ${token}` }
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = {
    ...await authHeaders(),
    ...init?.headers,
  }
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
export function subscribeEvents(onEvent: (e: BridgeEvent) => void): () => void {
  const es = new EventSource('/api/events')
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

export interface PurchaseResult {
  purchase_id: string
  status: string
  delivery_error: string
}

export class AlreadyOwnedError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AlreadyOwnedError'
  }
}

async function postPurchase(body: Record<string, string>): Promise<PurchaseResult> {
  const headers = {
    ...await authHeaders(),
    'Content-Type': 'application/json',
  }
  const res = await fetch('/api/marketplace/purchase', {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
  })
  if (res.status === 409) {
    const err = await res.json().catch(() => ({ message: 'Already owned' }))
    throw new AlreadyOwnedError(err.message || 'Already owned')
  }
  if (!res.ok) {
    throw new Error(`API ${res.status}: ${await res.text()}`)
  }
  return res.json()
}

export async function purchaseAlbum(albumId: string) {
  return postPurchase({ album_id: albumId })
}

export async function purchaseTrack(trackId: string) {
  return postPurchase({ track_id: trackId })
}
