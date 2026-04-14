# Bridge Music Server

A self-hosted music server that integrates with the Bridge Music marketplace. Users buy music through the Bridge Music iOS app, and purchased tracks download automatically to their home server for streaming.

Built on [Navidrome](https://github.com/navidrome/navidrome) as an embedded engine — Navidrome handles library management, transcoding, and Subsonic-compatible streaming. Bridge Music Server wraps it with purchase delivery, marketplace auth, and a branded frontend.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Docker Container (bridge-server:latest)                    │
│                                                             │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  bridge-server (:8080, exposed)                        │ │
│  │                                                        │ │
│  │  /              → embedded frontend (SPA)              │ │
│  │  /api/*         → Bridge API (purchases, auth, config) │ │
│  │  /api/webhook/* → Supabase purchase webhooks           │ │
│  │  /rest/*        → proxy → Navidrome Subsonic API       │ │
│  │  /nd/*          → proxy → Navidrome native API         │ │
│  │  /ws            → WebSocket (live library updates)     │ │
│  └──────────────┬─────────────────────────────────────────┘ │
│                 │ http://127.0.0.1:4533                     │
│  ┌──────────────▼─────────────────────────────────────────┐ │
│  │  navidrome (:4533, localhost only)                      │ │
│  │                                                        │ │
│  │  Library management, scanner, transcoder, Subsonic API │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  Volumes:                                                   │
│    /data/music      ← shared: sidecar writes, ND reads     │
│    /data/navidrome  ← ND database + cache                  │
│    /data/bridge     ← sidecar state (creds, queue, config) │
└─────────────────────────────────────────────────────────────┘
```

### Why this architecture?

- **No Navidrome fork.** We pin a stock Navidrome binary by version. Upstream releases are a one-line Dockerfile change. Zero merge conflicts, ever.
- **Single container.** Users run one `docker run` command. s6-overlay manages both processes internally.
- **Full branding.** Users interact with the Bridge Music frontend exclusively. Navidrome's UI is never exposed. The product feels like Bridge Music, not a wrapper.
- **Navidrome is the engine.** It handles the hard problems — audio transcoding, tag parsing, library indexing, Subsonic protocol. We don't reimplement any of that.

## System Components

### 1. Navidrome (embedded engine)

Stock binary from `deluan/navidrome:v0.61.1`. Runs on `127.0.0.1:4533`, not reachable from outside the container.

**What we use from it:**
- `/ping` — unauthenticated health check (readiness probe)
- `/auth/createAdmin` — first-run admin user creation (POST, only works when 0 users exist)
- `/rest/startScan` — trigger library scan after downloads (Subsonic API, requires admin auth)
- `/rest/getScanStatus` — poll scan progress
- `/rest/stream` — audio streaming (proxied through our server with injected auth)
- Native API (`/api/*`) — library browsing, playlist management, user data

**Auth model:** Navidrome uses JWT tokens via the `X-ND-Authorization` header (native API) and classic Subsonic token auth (`u=`, `t=`, `s=` params) for the Subsonic API. Our server holds the admin credentials and injects auth on every proxied request — end users never have or see Navidrome credentials.

**Config:** All via environment variables with `ND_` prefix. Key ones we set:
- `ND_ADDRESS=127.0.0.1` (localhost only)
- `ND_PORT=4533`
- `ND_MUSICFOLDER=/data/music`
- `ND_DATAFOLDER=/data/navidrome`

### 2. Bridge Server (sidecar — this codebase)

The Go binary that is the actual product. Handles:

#### Authentication
- Verifies Supabase JWTs from the iOS app and web frontend
- Bootstraps a Navidrome admin user on first run
- Translates Bridge Music identity → Navidrome sessions (users never touch ND auth)

#### Purchase Delivery
- Receives webhooks from Supabase when a purchase completes
- Falls back to polling for servers behind NAT (no public webhook endpoint)
- Downloads audio files from signed Supabase Storage URLs
- Writes files atomically to `/data/music/Bridge/{artist}/{album}/{track}.flac`
- Triggers Navidrome library scan after download completes
- Tracks purchase state machine: `queued → downloading → written → scanning → complete`

#### Reverse Proxy
- Proxies `/rest/*` and `/nd/*` to Navidrome with credentials injected
- Proxies `/rest/stream` for audio playback
- Serves the embedded frontend SPA at `/`

#### WebSocket
- Pushes live events to connected frontends: "library updated", "download progress", "scan complete"

### 3. Frontend (embedded SPA)

A web UI served by the sidecar at `/`. Built separately (React/Svelte/whatever you choose), compiled to static assets, embedded into the Go binary via `//go:embed`.

**Talks to:**
- `/api/*` — Bridge-specific endpoints (purchases, account linking, config)
- `/rest/*` — Navidrome Subsonic API (library browsing, playback)
- `/ws` — live updates

### 4. Supabase (external backend)

Your existing Supabase project, shared with the iOS app. Bridge Server connects to it for:
- JWT verification (shared Supabase JWT secret)
- Purchase webhook delivery (Supabase Edge Functions → user's server)
- Signed download URLs for purchased audio files
- Purchase history and entitlement lookups

## Data Flow: Purchase → Playback

```
1. User taps "Buy" in Bridge Music iOS app
2. iOS app completes payment (StoreKit / Supabase)
3. Supabase Edge Function fires webhook → user's Bridge Server
        POST /api/webhook/purchase
        {
          "purchase_id": "abc-123",
          "user_id": "user-456",
          "tracks": [
            {
              "track_id": "trk-789",
              "artist": "Artist Name",
              "album": "Album Title",
              "title": "Track Title",
              "format": "flac",
              "download_url": "https://xxx.supabase.co/storage/v1/...",
              "size_bytes": 45000000,
              "sha256": "a1b2c3..."
            }
          ],
          "signature": "hmac-sha256-signature"
        }

4. Bridge Server verifies webhook signature
5. Enqueues download tasks (persisted to local SQLite/BoltDB)
6. Download worker:
   a. Fetches file → /data/music/.incoming/{uuid}.part
   b. Verifies SHA-256 checksum
   c. Atomic rename → /data/music/Bridge/Artist Name/Album Title/Track Title.flac
   d. Marks task as "written"
7. Scan trigger:
   a. POST /rest/startScan to Navidrome (localhost)
   b. Poll /rest/getScanStatus until idle
   c. Mark task as "complete"
8. Push "library_updated" event over WebSocket to frontend
9. Track is now browsable and streamable through the UI
```

### Poll fallback (no public IP)

Many home servers sit behind NAT with no inbound connectivity. For these users:

```
Bridge Server runs a background poller (configurable interval, default 5 min):
  GET Supabase → /rest/v1/purchases?user_id=eq.{id}&status=eq.pending&order=created_at
  For each new purchase → enqueue download (same flow as webhook, step 5 onward)
  After processing → PATCH purchase status to "delivered"
```

Users configure either webhook mode (if they have a public URL/tunnel) or poll mode in their Bridge Server settings.

## First-Run Bootstrap

When the container starts with empty `/data/bridge`:

```
1. s6-overlay starts Navidrome process
2. s6-overlay starts Bridge Server (depends on Navidrome)
3. Bridge Server waits for Navidrome's /ping to return 200
4. Bridge Server checks /data/bridge/nd-credentials
   └─ Missing → first-run:
      a. Generate 32-byte random password
      b. POST /auth/createAdmin to Navidrome
         { "username": "bridge-admin", "password": "<generated>" }
      c. Store credentials in /data/bridge/nd-credentials (mode 0600)
5. Bridge Server authenticates to Navidrome, caches JWT
6. Bridge Server starts serving on :8080
```

**Important:** `/data/bridge` is critical state. If users lose this volume, they lose the Navidrome admin credentials and must reset. Document this clearly.

Alternative: set `ND_DEVAUTOCREATEADMINPASSWORD` from a deterministic secret derived from a user-provided `BRIDGE_SECRET` env var. This makes the admin password recoverable.

## Go Package Layout

```
bridge-server/
├── cmd/
│   └── bridge-server/
│       └── main.go                 # wire dependencies, start HTTP server
├── internal/
│   ├── config/
│   │   └── config.go              # env var loading (BRIDGE_ prefix)
│   ├── navidrome/
│   │   ├── client.go              # HTTP client for ND native + subsonic API
│   │   ├── bootstrap.go           # first-run admin creation, credential storage
│   │   ├── proxy.go               # reverse-proxy with credential injection
│   │   └── scan.go                # startScan + poll getScanStatus
│   ├── store/
│   │   ├── downloader.go          # download worker: fetch, verify, atomic write
│   │   ├── queue.go               # persistent task queue (SQLite)
│   │   └── models.go              # Purchase, DownloadTask, state machine
│   ├── supabase/
│   │   ├── client.go              # Supabase REST client
│   │   ├── webhook.go             # signature verification
│   │   └── jwt.go                 # JWT verification (shared secret)
│   ├── auth/
│   │   └── middleware.go          # extract + verify Supabase JWT from requests
│   ├── api/
│   │   ├── router.go             # top-level HTTP routing
│   │   ├── webhook.go            # POST /api/webhook/purchase handler
│   │   ├── purchases.go          # GET /api/purchases (history)
│   │   ├── settings.go           # GET/PUT /api/settings
│   │   └── ws.go                 # WebSocket upgrade + event broadcast
│   └── poller/
│       └── poller.go             # background poll worker for NAT'd servers
├── web/
│   └── dist/                     # frontend build output (embedded via go:embed)
├── docker/
│   └── s6-rc.d/                  # s6-overlay service definitions
├── Dockerfile
├── docker-compose.yml
├── .gitignore
├── PROJECT.md                    # this file
└── go.mod
```

## Configuration

All Bridge Server config uses the `BRIDGE_` env var prefix:

| Variable | Default | Description |
|----------|---------|-------------|
| `BRIDGE_PORT` | `8080` | Port the server listens on |
| `BRIDGE_DATA` | `/data/bridge` | Sidecar state directory |
| `BRIDGE_MUSIC_DIR` | `/data/music` | Music library (shared with ND) |
| `BRIDGE_SUPABASE_URL` | (required) | Supabase project URL |
| `BRIDGE_SUPABASE_ANON_KEY` | (required) | Supabase anon key |
| `BRIDGE_SUPABASE_SERVICE_KEY` | (required) | Supabase service role key (server-side only) |
| `BRIDGE_WEBHOOK_SECRET` | (required) | HMAC secret for webhook signature verification |
| `BRIDGE_DELIVERY_MODE` | `poll` | `webhook` or `poll` |
| `BRIDGE_POLL_INTERVAL` | `5m` | Poll interval (only in poll mode) |
| `BRIDGE_SERVER_ID` | (required in poll mode) | Unique id for this home server; marketplace writes `purchases.server_id = <this>` |
| `BRIDGE_SECRET` | (optional) | Master secret for deterministic ND admin password |
| `BRIDGE_ND_URL` | `http://127.0.0.1:4533` | Navidrome internal URL (don't change in Docker) |

Navidrome config uses `ND_` prefix — set in the Dockerfile, users shouldn't need to touch these.

## Deployment

### Docker (recommended)

```bash
docker run -d \
  --name bridge-music \
  -p 8080:8080 \
  -v ./music:/data/music \
  -v ./navidrome:/data/navidrome \
  -v ./bridge:/data/bridge \
  -e BRIDGE_SUPABASE_URL=https://your-project.supabase.co \
  -e BRIDGE_SUPABASE_ANON_KEY=eyJ... \
  -e BRIDGE_SUPABASE_SERVICE_KEY=eyJ... \
  -e BRIDGE_WEBHOOK_SECRET=your-webhook-secret \
  bridgemusic/bridge-server:latest
```

### Docker Compose

```yaml
services:
  bridge-music:
    image: bridgemusic/bridge-server:latest
    ports:
      - "8080:8080"
    volumes:
      - ./music:/data/music
      - ./navidrome:/data/navidrome
      - ./bridge:/data/bridge
    environment:
      BRIDGE_SUPABASE_URL: https://your-project.supabase.co
      BRIDGE_SUPABASE_ANON_KEY: ${SUPABASE_ANON_KEY}
      BRIDGE_SUPABASE_SERVICE_KEY: ${SUPABASE_SERVICE_KEY}
      BRIDGE_WEBHOOK_SECRET: ${WEBHOOK_SECRET}
    restart: unless-stopped
```

## Upstream Tracking

Navidrome is pinned by Docker image tag in the Dockerfile. To update:

1. Check [Navidrome releases](https://github.com/navidrome/navidrome/releases) for breaking changes
2. Update the `FROM deluan/navidrome:vX.Y.Z AS navidrome` line in the Dockerfile
3. Test locally: `docker compose build && docker compose up`
4. If Navidrome changed any Subsonic/native API endpoints we use, update `internal/navidrome/client.go`

**Endpoints we depend on** (monitor these in release notes):
- `GET /ping` — health check (stable, part of Subsonic spec)
- `POST /auth/createAdmin` — first-run only (Navidrome-specific)
- `POST /rest/startScan` — Subsonic API (stable)
- `GET /rest/getScanStatus` — Subsonic API (stable)
- `GET /rest/stream` — Subsonic API (stable)
- `POST /auth/login` — JWT mint (Navidrome-specific)
- Native API endpoints — for library browsing (Navidrome-specific, may change between versions)

## Scope (as of 2026-04-14)

The **marketplace storefront** (browse catalog, cart, Stripe checkout) lives in a
**separate repo** — a React Native app that ships as a tab inside the Bridge Music
iOS SwiftUI app, plus a web storefront sharing the same RN/React codebase. Both
surfaces talk directly to Supabase for catalog reads and Stripe for payments; a
successful Stripe webhook writes a `purchases` row which Supabase then fans out
to user home servers.

**This repo's job** is the home-server sidecar only:
- Wrap Navidrome, receive purchase events from Supabase (webhook or poll),
  download tracks, trigger scans, stream playback to the embedded web UI.
- The embedded `/marketplace` page and `POST /api/marketplace/purchase` endpoint
  in this repo are **dev-only test harnesses** that simulate the RN/web buy flow
  end-to-end without Stripe — they are not the production purchase surface.

## Development Milestones

### M1: Foundation — ✅ complete
- [x] Sidecar boots, starts Navidrome, waits for `/ping`
- [x] First-run bootstrap creates admin user, stores credentials
- [x] Sidecar authenticates to Navidrome, caches JWT
- [x] Reverse proxy works: `/rest/*` and `/nd/*` forward to Navidrome with injected auth
- [x] Health check endpoint on sidecar: `GET /api/health`
- [x] Config loading from env vars

### M2: Purchase Delivery — ✅ core flow working end-to-end in dev
- [x] Persistent download queue (SQLite via modernc.org/sqlite)
- [x] Webhook handler: signature verification, task enqueue (`POST /api/webhook/purchase`)
- [x] Download worker: fetch, SHA-256 verify, atomic write to music dir
- [x] Scan trigger: call `startScan`, poll until idle
- [x] Purchase history endpoint: `GET /api/purchases` (with embedded album/track metadata + delivery summary)
- [x] Entitlements endpoint: `GET /api/entitlements`
- [x] Redeliver endpoint: `POST /api/purchases/{id}/redeliver`
- [x] Per-track + whole-album download endpoints: `/api/tracks/{id}/download`, `/api/albums/{id}/zip`
- [x] Supabase `deliver-purchase` Edge Function (fans purchase → server webhook)
- [ ] Poll fallback worker: wire up real `server_id` registration (currently hard-coded `"TODO"` in `poller.go`)
- [ ] Signed-URL expiry handling: refresh the URL on 403 mid-download instead of failing the task

### M3: Frontend Shell — ✅ dev UI complete
- [x] Framework chosen: React 19 + TypeScript + Vite
- [x] Auth flow: Supabase login → JWT; runtime config fetched from `/api/config`
- [x] Library browsing via proxied Subsonic API (artists, albums, playlists)
- [x] Audio playback via proxied `/rest/stream` with global `PlayerContext` + persistent footer player
- [x] Purchase history page with per-item download buttons + redeliver
- [x] Settings page (delivery mode, poll interval, server status)
- [x] **[dev harness]** embedded marketplace page for E2E testing — will be removed or hidden once the RN/web storefront is live

### M4: Live Updates + Polish
- [x] Live event channel at `GET /api/events` (Server-Sent Events; chosen over raw WebSockets because we only push server→client and want zero deps)
- [x] Frontend receives `task_status` / `library_updated` / `purchase_enqueued` events — Purchases page auto-refreshes on any delivery change (replaces the old 5s polling fallback)
- [ ] Library page subscribes to `library_updated` and refreshes artists/albums automatically
- [ ] Per-task byte-count progress pushed during download (currently only emitted at status transitions: downloading/written/scanning/complete)
- [ ] Retry UI for individual failed tasks (whole-purchase redeliver exists; per-track retry does not)
- [ ] Reconcile-on-boot sweep: mark purchases as `delivered`/`failed` based on queue state at startup

### M5: Packaging + Docs
- [ ] s6-overlay service definitions under `docker/s6-rc.d/` (referenced in Go Package Layout, not yet created)
- [ ] Single-container Dockerfile bundling Navidrome + Bridge Server
- [ ] Multi-arch Docker build (amd64 + arm64 for NAS/Pi users) via CI
- [ ] Docker Hub / GHCR publishing pipeline
- [ ] User-facing setup docs (distinct from `DEV.md` which targets contributors)
- [ ] iOS app: "Connect Home Server" flow (enter server URL, verify connectivity, persist base URL)

### M6: Storefront Integration (new — tracks the separate RN/web repo)
- [x] Initial purchase contract spec drafted — see [`docs/PURCHASE_CONTRACT.md`](docs/PURCHASE_CONTRACT.md)
- [x] `BRIDGE_SERVER_ID` config wired end-to-end (poller, marketplace dev harness); required in poll mode outside dev
- [ ] Formalise `server_id` → user-home-server mapping table (likely `user_home_servers` in the storefront repo); first-boot QR/pairing-code UI on this server
- [ ] HMAC shared secret provisioning: how a freshly-installed home server registers itself and receives `BRIDGE_WEBHOOK_SECRET` (per-server secrets, see §6 of the contract)
- [ ] Stripe webhook → Supabase → home server flow tested end-to-end with a real Stripe test-mode payment (owned by the RN/web repo, validated against this server)
- [ ] Remove / feature-flag the dev-mode embedded marketplace UI once the RN app ships

## Security Considerations

- **Webhook signature verification is mandatory.** Never process unsigned webhooks. Use HMAC-SHA256 with `BRIDGE_WEBHOOK_SECRET`.
- **Navidrome admin credentials stay inside the container.** Written to `/data/bridge/nd-credentials` with `0600` perms. Never logged, never exposed via API.
- **Supabase service key is server-side only.** Never sent to the frontend. The anon key is used for client-side auth.
- **Download URLs are signed and short-lived.** Supabase Storage signed URLs expire. The download worker should handle expired URLs by requesting a fresh one.
- **Path traversal prevention.** When writing files from purchase metadata (artist/album/title), sanitize all path components. Strip `..`, `/`, null bytes, and OS-reserved characters.
- **File size limits.** Reject downloads over a configurable max (default 500MB) to prevent disk-fill attacks from a compromised webhook.

## Future Considerations (not in scope for initial release)

- **Multi-user.** Map Bridge accounts → individual Navidrome users. Per-user libraries, playlists, play history.
- **Navidrome plugin.** If upstream adds a `host_filesystem` plugin capability, migrate download+scan logic from sidecar to a pure `.ndp` Wasm plugin.
- **Offline sync.** iOS app downloads tracks for offline playback, with DRM or entitlement checks.
- **Artist dashboard.** Artists upload through a web portal; tracks become available in the store.
- **Social features.** Shared playlists, listening activity, recommendations.
