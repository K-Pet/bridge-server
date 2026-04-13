// Subsonic API client — all requests go through the Bridge Server proxy at /rest/*

const BASE_PARAMS = 'f=json&v=1.16.1&c=bridge-web'

function subsonicUrl(endpoint: string, extra?: Record<string, string>): string {
  const params = new URLSearchParams(extra)
  return `/rest/${endpoint}?${BASE_PARAMS}&${params.toString()}`
}

function unwrap<T>(data: unknown, key: string): T {
  const resp = (data as Record<string, unknown>)?.['subsonic-response'] as Record<string, unknown> | undefined
  if (!resp || resp.status !== 'ok') {
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
  const artists = unwrap<{ index: ArtistIndex[] }>(data, 'artists')
  return (artists.index ?? []).flatMap(idx => idx.artist ?? [])
}

export async function getArtist(id: string): Promise<{ artist: Artist; albums: Album[] }> {
  const res = await fetch(subsonicUrl('getArtist', { id }))
  const data = await res.json()
  const result = unwrap<Artist & { album?: Album[] }>(data, 'artist')
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
  const result = unwrap<{ album?: Album[] }>(data, 'albumList2')
  return result.album ?? []
}

export async function getAlbum(id: string): Promise<{ album: Album; songs: Song[] }> {
  const res = await fetch(subsonicUrl('getAlbum', { id }))
  const data = await res.json()
  const result = unwrap<Album & { song?: Song[] }>(data, 'album')
  const { song, ...album } = result
  return { album, songs: song ?? [] }
}

export async function getRandomSongs(size = 50): Promise<Song[]> {
  const res = await fetch(subsonicUrl('getRandomSongs', { size: String(size) }))
  const data = await res.json()
  const result = unwrap<{ song?: Song[] }>(data, 'randomSongs')
  return result.song ?? []
}

export async function getPlaylists(): Promise<Playlist[]> {
  const res = await fetch(subsonicUrl('getPlaylists'))
  const data = await res.json()
  const result = unwrap<{ playlist?: Playlist[] }>(data, 'playlists')
  return result.playlist ?? []
}

export async function getPlaylist(id: string): Promise<PlaylistWithSongs> {
  const res = await fetch(subsonicUrl('getPlaylist', { id }))
  const data = await res.json()
  return unwrap<PlaylistWithSongs>(data, 'playlist')
}

export async function search(query: string): Promise<SearchResult> {
  const res = await fetch(subsonicUrl('search3', { query, artistCount: '5', albumCount: '10', songCount: '20' }))
  const data = await res.json()
  return unwrap<SearchResult>(data, 'searchResult3')
}

// --- URL builders (no fetch, used for <img> and <audio>) ---

export function coverArtUrl(id: string, size = 300): string {
  return `/rest/getCoverArt?${BASE_PARAMS}&id=${encodeURIComponent(id)}&size=${size}`
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
