import { createClient, type SupabaseClient } from '@supabase/supabase-js'

export interface AppConfig {
  supabase_url: string
  supabase_anon_key: string
  dev_mode: boolean
  marketplace_url: string
  // Empty string when the project doesn't enforce captcha (e.g. local
  // dev). The frontend treats empty as "captcha disabled" and skips
  // the widget — matches the behavior of the local Supabase config
  // where [auth.captcha] is commented out.
  hcaptcha_site_key: string
  // Dev-only: test credentials for auto-sign-in (never present in prod).
  dev_email?: string
  dev_password?: string
}

let _supabase: SupabaseClient | null = null
let _config: AppConfig | null = null

/** Fetch config from the Go backend and initialize the Supabase client. */
export async function initConfig(): Promise<AppConfig> {
  const res = await fetch('/api/config')
  if (!res.ok) throw new Error(`Failed to load config: ${res.status}`)
  _config = await res.json()

  if (_config!.supabase_url && _config!.supabase_anon_key) {
    _supabase = createClient(_config!.supabase_url, _config!.supabase_anon_key, {
      auth: {
        // Persist sessions in localStorage and auto-refresh before expiry.
        // PKCE flow is the modern default for browser apps — protects
        // recovery / magic-link redirects from token interception.
        persistSession: true,
        autoRefreshToken: true,
        detectSessionInUrl: true,
        flowType: 'pkce',
      },
    })
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
