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

export async function getPurchases() {
  return apiFetch<unknown[]>('/api/purchases')
}

export async function getSettings() {
  return apiFetch<{ delivery_mode: string; poll_interval: string }>('/api/settings')
}

export interface PurchaseResult {
  purchase_id: string
  status: string
  delivery_error: string
}

export async function purchaseAlbum(albumId: string) {
  return apiFetch<PurchaseResult>('/api/marketplace/purchase', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ album_id: albumId }),
  })
}

export async function purchaseTrack(trackId: string) {
  return apiFetch<PurchaseResult>('/api/marketplace/purchase', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ track_id: trackId }),
  })
}
