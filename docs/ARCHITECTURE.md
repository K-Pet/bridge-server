# Architecture — bridge-server

This document covers the **server-side internals**. For the
ecosystem-wide picture (how marketplace + server + iOS app fit
together) see
[`../../Bridge-Music-Marketplace/docs/ARCHITECTURE.md`](../../Bridge-Music-Marketplace/docs/ARCHITECTURE.md).

For the wire protocol with the marketplace see
[`../../Bridge-Music-Marketplace/docs/reference/PURCHASE_CONTRACT.md`](../../Bridge-Music-Marketplace/docs/reference/PURCHASE_CONTRACT.md).

---

## 1. Process model

A single Docker container runs **two processes** under
[s6-overlay](https://github.com/just-containers/s6-overlay):

```
┌─────────────────────────────────────────────────────────────┐
│  Docker Container (ghcr.io/k-pet/bridge-server:latest)      │
│                                                             │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  bridge-server (Go) — :8888, exposed                   │ │
│  │                                                        │ │
│  │  /              → embedded React SPA (admin UI)        │ │
│  │  /api/*         → Bridge API (purchases, auth, config) │ │
│  │  /api/webhook/* → marketplace purchase webhook         │ │
│  │  /api/events    → SSE for live UI updates              │ │
│  │  /rest/*        → reverse proxy → Navidrome Subsonic   │ │
│  │  /nd/*          → reverse proxy → Navidrome native     │ │
│  └──────────────┬─────────────────────────────────────────┘ │
│                 │ http://127.0.0.1:4533                     │
│  ┌──────────────▼─────────────────────────────────────────┐ │
│  │  navidrome (stock binary) — :4533, localhost only      │ │
│  │  Library indexing, transcoding, Subsonic API           │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  Volumes:                                                   │
│    /data/music      ← bridge-server writes; ND reads        │
│    /data/navidrome  ← ND database + cache                   │
│    /data/bridge     ← server state (creds, queue, config)   │
└─────────────────────────────────────────────────────────────┘
```

**Why this shape:**

- **No Navidrome fork.** We pin a stock Navidrome image (`deluan/navidrome`)
  in the Dockerfile. Upstream releases are a one-line bump. Zero merge
  conflicts ever.
- **Single container.** One `docker run`. s6 manages both processes.
- **Navidrome's UI is never exposed.** It binds to localhost; users
  hit our React SPA which proxies through.
- **Navidrome is the engine, we're the product.** Audio transcoding,
  tag parsing, library indexing, the Subsonic protocol — we don't
  reimplement any of it.

## 2. Repo layout

```
cmd/bridge-server/main.go        wire dependencies, start HTTP server
internal/
├── config/                      env vars + auto-minted credentials
│   ├── config.go                envStr() + envBool() + linker-baked defaults
│   ├── credentials.go           load/mint /data/bridge/credentials.json
│   └── config_test.go
├── auth/middleware.go           Supabase JWT verification middleware
├── api/
│   ├── router.go                http.ServeMux setup (auth + public groups)
│   ├── webhook.go               POST /api/webhook/purchase  (HMAC verify, enqueue)
│   ├── purchases.go             GET /api/purchases, redeliver, track download
│   ├── library.go               DELETE /api/library/{songs,albums}/{id}
│   ├── events.go                GET /api/events  (Server-Sent Events)
│   ├── pair.go                  GET /api/pair, POST /api/pair/generate (legacy)
│   ├── onboarding.go            /api/auto-pair, /api/pair/status, /api/onboarding/*
│   └── helpers.go               JSON helpers
├── store/
│   ├── downloader.go            fetch → SHA-256 → atomic rename → scan
│   ├── queue.go                 SQLite-backed task queue
│   └── models.go                Purchase, Track, DownloadTask
├── navidrome/
│   ├── client.go                Subsonic + native API client
│   ├── bootstrap.go             first-run admin user creation
│   ├── proxy.go                 reverse proxy with credential injection
│   └── scan.go                  /rest/startScan + getScanStatus polling
├── supabase/
│   ├── client.go                marketplace EF client (HMAC + JWT)
│   ├── webhook.go               HMAC verify for inbound webhooks
│   ├── hmac.go                  HMAC sign for outbound EF calls
│   └── jwt.go                   JWT verifier (calls /auth/v1/user)
└── poller/poller.go             5-min poll worker (NAT'd installs)

frontend/
├── src/
│   ├── App.tsx                  root + auth gate
│   ├── lib/
│   │   ├── supabase.ts          Supabase JS client
│   │   └── api.ts               apiFetch() — all /api calls go through here
│   ├── components/Layout.tsx    sidebar + content shell
│   └── pages/
│       ├── Login.tsx            Supabase email/password
│       ├── Library.tsx          Subsonic-driven library browser
│       ├── Purchases.tsx        purchase history + redeliver
│       └── Settings.tsx         server config + status
└── vite.config.ts               dev proxy → :8888

web/dist/                        frontend build output (embedded via go:embed)

docker/
└── s6-rc.d/                     s6 service definitions
    ├── navidrome/run            navidrome launcher (loads container env)
    ├── bridge-server/run        bridge-server launcher
    ├── user/contents.d/         active services list
    └── */dependencies.d/        startup ordering

Dockerfile                       multi-stage: navidrome → frontend → sidecar → runtime
docker-compose.yml               production single-container compose
docker-compose.dev.yml           local dev (Navidrome only — bridge-server runs from `go run`)
Tiltfile                         local dev orchestration
```

## 3. Boot sequence

When the container starts cold (empty volumes):

```
1. s6-overlay starts.
2. s6 starts navidrome  (run script loads container env, exec /app/navidrome)
3. s6 starts bridge-server (depends on navidrome — waits for /app/ to respond)
4. bridge-server boots:
   a. Loads env (BRIDGE_*)
   b. Calls loadOrMintCredentials() — reads/creates /data/bridge/credentials.json
      Mints a fresh server_id (16 hex bytes) and webhook_secret (32 hex bytes)
      on first run; subsequent boots reuse the persisted values.
   c. Connects to Navidrome — if no admin exists yet, POSTs /auth/createAdmin
      with a random password; persists creds to /data/bridge/nd-credentials (0600).
   d. Authenticates to Navidrome, caches the JWT.
   e. Starts the download worker (consumes from /data/bridge/queue.db).
   f. Starts the SSE event hub.
   g. Optionally starts the poller (when BRIDGE_DELIVERY_MODE=poll).
   h. Listens on :8888.
```

**Critical**: both s6 service `run` scripts must explicitly load
`/run/s6/container_environment/*` before exec'ing their binary. s6
v3 does **not** inherit Dockerfile ENV automatically. Without this,
Navidrome silently falls back to relative-path defaults and stores a
broken `MusicFolder` in its DB on first scan. See `docker/s6-rc.d/*/run`
for the workaround (the `with-contenv` helper has a broken execline
path on this image).

## 4. Purchase delivery — internals

The marketplace's `deliver-purchase` Edge Function POSTs to:

```
POST ${BRIDGE_EXTERNAL_URL}/api/webhook/purchase
X-Bridge-Signature: <hex HMAC-SHA256 of body>
Content-Type: application/json
```

`internal/api/webhook.go` handler:

1. **Verify signature.** `internal/supabase/webhook.go::VerifyAndParse`
   reads the raw body (capped at 1 MB), recomputes HMAC-SHA256 with
   `cfg.WebhookSecret`, constant-time compares. Rejects 401 on
   mismatch.
2. **Replay window.** Body's `timestamp` field must be within
   `MaxWebhookAge` (5 minutes). Rejects 401 outside the window.
3. **Idempotency check.** If the queue already has all tasks for this
   `purchase_id` and they're all `complete` AND every expected file
   still exists on disk, short-circuits with `{"status": "already_delivered"}`.
4. **Reset prior tasks.** A redeliver replaces previous queue entries
   for the purchase (the task primary key is `<purchase_id>:<track_id>`,
   so re-insert without delete would hit a UNIQUE violation).
5. **Enqueue.** One row per track in the payload.
6. **Publish event.** SSE hub broadcasts `purchase_enqueued` to any
   connected admin SPA tab.
7. Returns `{"status": "accepted"}` 200.

The actual download happens **async** in `internal/store/downloader.go`:

```
loop:
  task = queue.Next()
  status = downloading
  fetch task.Track.DownloadURL → /data/music/.incoming/<uuid>.part
    (handles 403 expired-URL by refetching from get-download-urls EF)
  verify SHA-256 against task.Track.SHA256
  status = written
  os.Rename → /data/music/Bridge/<artist>/<album>/<title>.<format>
  status = scanning
  POST /rest/startScan to Navidrome (waits up to 10 min)
  status = complete
  → mark-purchase-status (delivered)
```

`/data/music/Bridge/<artist>/<album>/...` is the canonical layout —
all sanitization (path traversal stripping, OS-reserved char
replacement) happens in `store.ExpectedTrackPath` and `store.sanitize`.

## 5. Authentication

bridge-server has three auth contexts:

### 5a. End user → bridge-server

The admin SPA + iOS app authenticate with a **Supabase user JWT**.
`internal/auth/middleware.go` extracts the `Bearer` token, calls
`${SUPABASE_URL}/auth/v1/user` to verify, and stuffs the user ID into
the request context via `auth.UserID(ctx)`.

In dev (`BRIDGE_DEV=true`), the middleware skips verification and
injects a fixed `dev-user`.

### 5b. Marketplace EF → bridge-server (inbound webhook)

HMAC-SHA256 over the raw request body, key = `cfg.WebhookSecret`,
header `X-Bridge-Signature: <hex>`. See §4 step 1.

### 5c. bridge-server → marketplace EF (outbound, server-context)

For `mark-purchase-status` and `poll-pending-purchases`, where no user
JWT exists. `internal/supabase/hmac.go::SignedRequester` adds:

```
Authorization: Bearer <SUPABASE_ANON_KEY>     (so Kong allows the request)
apikey:        <SUPABASE_ANON_KEY>
X-Bridge-Server-Id: <cfg.ServerID>
X-Bridge-Signature: <hex HMAC-SHA256(body, cfg.WebhookSecret)>
```

The body always includes a `timestamp` field for replay protection.
The marketplace EFs verify via `_shared/bridge-auth.ts`.

### 5d. bridge-server → marketplace EF (user-context)

For `register-home-server` and `get-home-server` during onboarding.
`internal/supabase/client.go::callUserEF` forwards the user's JWT as
`Bearer` so the EF can derive the `user_id` from it.

## 6. Navidrome integration

bridge-server **never exposes** Navidrome to end users directly. The
container binds Navidrome to `127.0.0.1:4533` only. All access goes
through bridge-server's reverse proxy:

| Request to bridge-server | Forwarded to Navidrome | Why |
|---|---|---|
| `GET /rest/*` | `GET /rest/*` | Subsonic API (used by clients + the iOS app) |
| `GET /nd/*` | `GET /api/*` | Navidrome native API (used by the admin SPA's library page) |
| `GET /rest/stream` | `GET /rest/stream` | Audio streaming |

`internal/navidrome/proxy.go` injects credentials on every proxied
request — clients never see Navidrome auth. The admin user is
auto-created on first run and stored at `/data/bridge/nd-credentials`
(mode 0600).

For scan triggers (after a download completes), bridge-server calls
`POST /rest/startScan` directly via `internal/navidrome/scan.go` and
polls `/rest/getScanStatus` until idle.

## 7. Storage layout

Three persistent paths, all volumes:

| Container path | Host source | Owner | Contents |
|---|---|---|---|
| `/data/music` | bind mount (`MUSIC_DIR` env) | shared | Music library — bridge-server writes, Navidrome reads |
| `/data/navidrome` | named volume `bridge-navidrome` | Navidrome | navidrome.db + cache |
| `/data/bridge` | named volume `bridge-data` | bridge-server | `credentials.json`, `nd-credentials`, `queue.db` |

The named volumes survive `docker compose down` + `up -d`. If you
delete `bridge-data`, you lose:

- The auto-minted `server_id` and `webhook_secret` — re-pairing
  generates new ones, but until the user re-pairs, the marketplace
  signs webhooks with the old (now-invalid) secret.
- The Navidrome admin password — bridge-server re-bootstraps a fresh
  one on next boot, but it differs from any backup.

## 8. Deployment modes

The same Docker image supports three deployment shapes:

### 8a. Self-host with public HTTPS (the production path)

User points DNS at their host, runs the image with
`BRIDGE_DELIVERY_MODE=webhook` (default), and terminates TLS via Caddy
or nginx. Marketplace pushes deliveries directly.

### 8b. Self-host behind NAT, no public URL

User runs with `BRIDGE_DELIVERY_MODE=poll` and `BRIDGE_POLL_INTERVAL=5m`.
bridge-server polls `poll-pending-purchases` on the schedule. No
inbound connectivity required.

### 8c. Cloudflare Tunnel for ecosystem testing

Same as 8a but using `cloudflared` for ingress. Free and avoids the DNS
+ TLS-cert dance. Useful for development; named tunnels for stability.

See [`DEPLOYMENT.md`](./DEPLOYMENT.md) for the full runbook.
