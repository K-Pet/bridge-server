// library-version.ts
//
// Tiny module-level counter that bumps whenever the server reports a
// library-state change (cover/photo upload, tag edit, scan finishing,
// etc.). Cover-art URLs include the current value as a query param so
// the browser cache can't serve stale image bytes when a Subsonic
// id stays the same but its underlying file has been replaced.
//
// Why module-level and not React state: cover URLs are computed
// in render across dozens of components, many of which don't share
// a parent. A single source-of-truth bumped on every library_updated
// event keeps them in sync without lifting state through the whole
// tree. Components naturally re-render on library_updated (they
// refetch their data), so the URL string they emit picks up the new
// value during that re-render — no subscription needed.

// Persisted to localStorage so the version stays stable across
// reloads — without persistence we'd bust the browser cache on every
// page load and re-download every visible cover for no reason. The
// version only changes when a library_updated event fires, which is
// the signal that a cover *might* have changed.
const STORAGE_KEY = 'bridge.library_version'

function loadInitial(): number {
  try {
    const raw = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
    const n = raw ? parseInt(raw, 10) : NaN
    if (Number.isFinite(n) && n > 0) return n
  } catch {
    // localStorage can throw in some privacy modes — fall through to
    // a fresh value, which means this session won't share cache with
    // any previous one but still benefits from in-session caching.
  }
  return Date.now()
}

let libraryVersion = loadInitial()

// getLibraryVersion returns the current cache-busting nonce. Called
// from coverArtUrl on every render. Cheap — just a read.
export function getLibraryVersion(): number {
  return libraryVersion
}

// bumpLibraryVersion invalidates cover-art caches across the app.
// Call this when the server confirms an image-bearing entity has
// changed: cover upload, artist photo upload, rename (which can
// shift coverArt ids), library_updated SSE events, etc.
export function bumpLibraryVersion(): void {
  // Date.now() resolution is 1ms — collisions only happen if multiple
  // bumps fire in the same millisecond, in which case the URL is
  // already going to refetch from the previous bump. Safe.
  libraryVersion = Date.now()
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, String(libraryVersion))
    }
  } catch {
    // ignore — in-memory value still works for the current session
  }
}
