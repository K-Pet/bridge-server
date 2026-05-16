// Subsonic API client — all requests go through the Bridge Server proxy at /rest/*

import { getLibraryVersion } from './library-version'

const BASE_PARAMS = 'f=json&v=1.16.1&c=bridge-web'

function subsonicUrl(endpoint: string, extra?: Record<string, string>): string {
  const params = new URLSearchParams(extra)
  return `/rest/${endpoint}?${BASE_PARAMS}&${params.toString()}`
}

// SubsonicNotFoundError is thrown when a single-resource fetch (album,
// artist, playlist) succeeds at the protocol level but the requested
// id no longer resolves. Distinct from a generic Subsonic error so
// callers can navigate away rather than render a scary error page —
// the common cause is a rename that shifted Navidrome's hash-based
// id, leaving the URL the user is sitting on stale.
export class SubsonicNotFoundError extends Error {
  constructor(public readonly key: string) {
    super(`Subsonic resource "${key}" not found`)
    this.name = 'SubsonicNotFoundError'
  }
}

function unwrap<T>(data: unknown, key: string): T {
  const resp = (data as Record<string, unknown>)?.['subsonic-response'] as Record<string, unknown> | undefined
  if (!resp || resp.status !== 'ok') {
    // Subsonic error code 70 = "Data not found" — treat it as the
    // typed not-found so the UI can recover, not a hard error.
    const err = resp?.error as { code?: number; message?: string } | undefined
    if (err?.code === 70) {
      throw new SubsonicNotFoundError(key)
    }
    throw new Error(`Subsonic error: ${JSON.stringify(resp?.error ?? 'unknown')}`)
  }
  return resp[key] as T
}

// --- Types ---

export interface Artist {
  id: string
  name: string
  albumCount: number
  coverArt?: string
  artistImageUrl?: string
}

export interface ArtistIndex {
  name: string
  artist: Artist[]
}

export interface Album {
  id: string
  name: string
  artist: string
  artistId: string
  coverArt?: string
  songCount: number
  duration: number
  year?: number
  genre?: string
  created: string
}

export interface Song {
  id: string
  title: string
  album: string
  albumId: string
  artist: string
  artistId: string
  track?: number
  discNumber?: number
  year?: number
  genre?: string
  duration: number
  size: number
  suffix: string
  contentType: string
  coverArt?: string
  bitRate?: number
}

export interface Playlist {
  id: string
  name: string
  songCount: number
  duration: number
  owner: string
  public: boolean
  created: string
  changed: string
  coverArt?: string
  comment?: string
}

export interface PlaylistWithSongs extends Playlist {
  entry: Song[]
}

export interface SearchResult {
  artist?: Artist[]
  album?: Album[]
  song?: Song[]
}

// --- API methods ---

export async function getArtists(): Promise<Artist[]> {
  const res = await fetch(subsonicUrl('getArtists'))
  const data = await res.json()
  // On an empty library Navidrome may omit the `artists` envelope
  // entirely (status:ok with no body). Default to an empty index list
  // rather than throwing — empty is a valid library state.
  const artists = unwrap<{ index?: ArtistIndex[] } | undefined>(data, 'artists')
  return (artists?.index ?? []).flatMap(idx => idx.artist ?? [])
}

export async function getArtist(id: string): Promise<{ artist: Artist; albums: Album[] }> {
  const res = await fetch(subsonicUrl('getArtist', { id }))
  const data = await res.json()
  const result = unwrap<(Artist & { album?: Album[] }) | undefined>(data, 'artist')
  if (!result || !result.id) {
    // Status was ok but the body is empty — happens when an id was
    // valid moments ago and a rename invalidated it before our
    // refresh round-tripped.
    throw new SubsonicNotFoundError('artist')
  }
  const { album, ...artist } = result
  return { artist, albums: album ?? [] }
}

export async function getAlbumList(
  type: 'newest' | 'recent' | 'frequent' | 'alphabeticalByName' | 'alphabeticalByArtist' | 'random' = 'newest',
  size = 50,
  offset = 0
): Promise<Album[]> {
  const res = await fetch(subsonicUrl('getAlbumList2', { type, size: String(size), offset: String(offset) }))
  const data = await res.json()
  const result = unwrap<{ album?: Album[] } | undefined>(data, 'albumList2')
  return result?.album ?? []
}

export async function getAlbum(id: string): Promise<{ album: Album; songs: Song[] }> {
  const res = await fetch(subsonicUrl('getAlbum', { id }))
  const data = await res.json()
  const result = unwrap<(Album & { song?: Song[] }) | undefined>(data, 'album')
  if (!result || !result.id) {
    throw new SubsonicNotFoundError('album')
  }
  const { song, ...album } = result
  return { album, songs: song ?? [] }
}

export async function getRandomSongs(size = 50): Promise<Song[]> {
  const res = await fetch(subsonicUrl('getRandomSongs', { size: String(size) }))
  const data = await res.json()
  const result = unwrap<{ song?: Song[] } | undefined>(data, 'randomSongs')
  return result?.song ?? []
}

export async function getPlaylists(): Promise<Playlist[]> {
  const res = await fetch(subsonicUrl('getPlaylists'))
  const data = await res.json()
  const result = unwrap<{ playlist?: Playlist[] } | undefined>(data, 'playlists')
  return result?.playlist ?? []
}

export async function getPlaylist(id: string): Promise<PlaylistWithSongs> {
  const res = await fetch(subsonicUrl('getPlaylist', { id }))
  const data = await res.json()
  const result = unwrap<PlaylistWithSongs | undefined>(data, 'playlist')
  if (!result || !result.id) {
    throw new SubsonicNotFoundError('playlist')
  }
  return result
}

export async function search(query: string): Promise<SearchResult> {
  const res = await fetch(subsonicUrl('search3', { query, artistCount: '5', albumCount: '10', songCount: '20' }))
  const data = await res.json()
  const result = unwrap<SearchResult | undefined>(data, 'searchResult3')
  return result ?? {}
}

// norm normalizes a (possibly null/undefined) string to a comparable
// lowercase form. Subsonic occasionally returns null for fields on
// "[Unknown Artist]" / "[Unknown Album]" rows; calling .trim() on
// those throws "Cannot read properties of null (reading 'trim')".
// Centralizing the coalesce makes every comparator below null-safe.
function norm(s: string | null | undefined): string {
  return (s ?? '').trim().toLowerCase()
}

// findAlbumByName looks up an album by its (possibly just-renamed)
// title and artist. Used by the rename recovery path: after an album
// edit changes Navidrome's hash-based id, search3 lets us find the
// new id so the user lands back on the same album they edited.
//
// Returns the first match where both name and artist agree
// case-insensitively. We don't trust scores alone — Subsonic search
// can return adjacent titles ahead of an exact match.
export async function findAlbumByName(name: string, artist: string): Promise<Album | null> {
  if (!name) return null
  const results = await search(name)
  const candidates = results.album ?? []
  const lowerName = norm(name)
  const lowerArtist = norm(artist)
  for (const a of candidates) {
    if (norm(a.name) === lowerName && norm(a.artist) === lowerArtist) {
      return a
    }
  }
  // Fall back to name-only match — artist might have shifted too in
  // the same edit (album_artist change cascades).
  for (const a of candidates) {
    if (norm(a.name) === lowerName) {
      return a
    }
  }
  return null
}

// findArtistByName is the artist-rename counterpart to findAlbumByName.
export async function findArtistByName(name: string): Promise<Artist | null> {
  if (!name) return null
  const results = await search(name)
  const candidates = results.artist ?? []
  const lowerName = norm(name)
  for (const a of candidates) {
    if (norm(a.name) === lowerName) return a
  }
  return null
}

// --- URL builders (no fetch, used for <img> and <audio>) ---

export function coverArtUrl(id: string, size = 300): string {
  // The `_v` param doesn't affect the Subsonic response (unknown params
  // are ignored) but changes whenever a library mutation lands, so the
  // browser image cache treats post-mutation URLs as new entries and
  // refetches the bytes. Without this a folder/embedded cover swap
  // looks like nothing happened until the user opens an incognito tab.
  return `/rest/getCoverArt?${BASE_PARAMS}&id=${encodeURIComponent(id)}&size=${size}&_v=${getLibraryVersion()}`
}

export function streamUrl(id: string): string {
  return `/rest/stream?${BASE_PARAMS}&id=${encodeURIComponent(id)}`
}

// --- Formatting helpers ---

export function formatDuration(seconds: number): string {
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m}:${s.toString().padStart(2, '0')}`
}

export function formatDurationLong(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h} hr ${m} min`
  return `${m} min`
}
