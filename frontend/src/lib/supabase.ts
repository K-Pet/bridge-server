import { createClient, type SupabaseClient } from '@supabase/supabase-js'

export interface AppConfig {
  supabase_url: string
  supabase_anon_key: string
  dev_mode: boolean
  marketplace_url: string
}

let _supabase: SupabaseClient | null = null
let _config: AppConfig | null = null

/** Fetch config from the Go backend and initialize the Supabase client. */
export async function initConfig(): Promise<AppConfig> {
  const res = await fetch('/api/config')
  if (!res.ok) throw new Error(`Failed to load config: ${res.status}`)
  _config = await res.json()

  if (_config!.supabase_url && _config!.supabase_anon_key) {
    _supabase = createClient(_config!.supabase_url, _config!.supabase_anon_key)
  }

  return _config!
}

export function getConfig(): AppConfig {
  if (!_config) throw new Error('Config not loaded — call initConfig() first')
  return _config
}

export function getSupabase(): SupabaseClient | null {
  return _supabase
}

export function isDevMode(): boolean {
  return _config?.dev_mode ?? false
}
