# Development — bridge-server

How to set up and run bridge-server locally on **macOS**, **Linux**,
and **Windows**.

For server-side architecture see [`ARCHITECTURE.md`](./ARCHITECTURE.md).
For production deployment (Docker + auto-pair) see
[`DEPLOYMENT.md`](./DEPLOYMENT.md).

> bridge-server **does not** own the Supabase schema. To work on
> anything that touches Supabase (catalog, purchases, Edge Functions),
> bring up the marketplace repo first — it provides the local Supabase
> stack this server connects to.

---

## 1. Prerequisites

- **Go 1.25 or newer** — `go version`
- **Node.js 20.x or newer** (for the embedded admin SPA)
- **Docker** (for Navidrome locally; the production image bundles it)
- **Tilt** (orchestrates the dev services)
- **Supabase CLI** (only used to verify the marketplace's local stack
  is running — bridge-server doesn't call it directly)
- **Git**

### macOS

```bash
brew install go node git
brew install --cask docker        # Docker Desktop
brew install supabase/tap/supabase
brew install tilt-dev/tap/tilt
```

### Linux (Ubuntu / Debian / WSL2)

```bash
# Go 1.25 — official static install (apt is usually behind)
curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -xz -C /usr/local
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc && source ~/.bashrc

# Node 20
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs git

# Docker Engine
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
    https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
    | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
sudo usermod -aG docker $USER
# Log out and back in

# Supabase CLI (static binary; not in apt)
curl -fsSL https://github.com/supabase/cli/releases/latest/download/supabase_linux_amd64.tar.gz \
    | sudo tar -xz -C /usr/local/bin supabase

# Tilt
curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
```

### Windows (WSL2)

Same as the marketplace setup — install **WSL2 + Ubuntu** and **Docker
Desktop** with WSL Integration enabled. Then follow the **Linux** steps
above inside the Ubuntu shell. Native PowerShell is not supported.

```powershell
wsl --install -d Ubuntu
# Install Docker Desktop, enable WSL Integration → Ubuntu in its settings
```

Once you're in the Ubuntu shell, follow the Linux instructions. Keep
the project under the Linux filesystem (`~/code/...`), not under
`/mnt/c/...` — cross-filesystem disk I/O kills the build cache.

---

## 2. Clone the marketplace repo too

bridge-server depends on the marketplace's local Supabase stack. Clone
both as siblings:

```bash
mkdir -p ~/OtherProjects && cd ~/OtherProjects
git clone https://github.com/<your-org>/Bridge-Music-Marketplace.git
git clone https://github.com/k-pet/bridge-server.git
```

---

## 3. First-time setup

```bash
cd ~/OtherProjects/bridge-server

# Install frontend deps
cd frontend && npm install && cd ..

# Create local data directories (gitignored)
mkdir -p data/music data/navidrome data/bridge

# Copy env template
cp .env.example .env.local
```

Open `.env.local` and confirm `BRIDGE_SUPABASE_URL=http://127.0.0.1:54321`.
The `BRIDGE_SUPABASE_ANON_KEY` matches the local-Supabase JWT printed
by `supabase status` after the marketplace's Tilt brings it up.

---

## 4. Start the dev stack

The marketplace **must be up first** — bridge-server's Tiltfile health-
checks `:54321` and refuses to start without it.

```bash
# Terminal A — marketplace (Supabase + storefront)
cd ~/OtherProjects/Bridge-Music-Marketplace && tilt up

# wait until supabase-seed reports "Seed complete"

# Terminal B — bridge-server
cd ~/OtherProjects/bridge-server && tilt up
```

Tilt opens its dashboard at **http://localhost:10350** (different port
from the marketplace's 10351). Resources:

| Service | URL | What it does |
|---|---|---|
| `navidrome` | http://localhost:4533 | Stock Navidrome via Docker Compose. In dev it IS exposed (so you can poke its UI directly); in production it's localhost-only. |
| `supabase` | — | Health-checks `:54321`, then exits 0. |
| `bridge-server` | http://localhost:8088 | Go backend in `BRIDGE_DEV=true` mode. Auto-restarts on changes under `cmd/` or `internal/`. |
| `frontend` | http://localhost:5173 | Vite dev server. HMR via the proxy in `frontend/vite.config.ts`. |

Open **http://localhost:5173**. The login screen takes the marketplace's
seeded user:

- **Email**: `test@bridge.music`
- **Password**: `testpass123`

In dev mode (`BRIDGE_DEV=true`), bridge-server:

- Skips Supabase JWT verification (every request runs as `dev-user`).
- Tolerates Navidrome being unavailable (returns empty libraries
  instead of erroring).
- Skips the Supabase poller.

This lets you iterate on the Go backend without a fully wired pairing
flow.

---

## 5. End-to-end purchase test (with marketplace + Stripe)

For a real delivery in dev:

1. Make sure both Tilts are up.
2. In the marketplace's `.env.local`, set `STRIPE_SECRET_KEY=sk_test_...`
   so `stripe-webhook` forwards events.
3. Run `stripe login` once.
4. From `http://localhost:8081` (the marketplace), buy a seeded album
   with the test card `4242 4242 4242 4242`.
5. Watch this repo's `bridge-server` logs in Tilt — you should see
   `purchase enqueued` → `downloading` → `scanning` → `complete`.
6. Refresh the bridge-server frontend's Library page — the album
   shows up.

If something fails, the marketplace's purchase-detail screen is the
clearest debug surface — it shows per-track delivery state and a
retry button. The `retry-purchase-delivery` Edge Function re-runs the
full fan-out.

---

## 6. Running without Tilt

If Tilt isn't your style:

```bash
# Terminal 1: Navidrome
docker compose -f docker-compose.dev.yml up

# Terminal 2: Go backend
mkdir -p data/bridge data/music
BRIDGE_DEV=true \
BRIDGE_PORT=8888 \
BRIDGE_DATA=./data/bridge \
BRIDGE_MUSIC_DIR=./data/music \
BRIDGE_ND_URL=http://localhost:4533 \
go run ./cmd/bridge-server

# Terminal 3: Frontend
cd frontend && npm run dev
```

This skips the Supabase health-check, so make sure marketplace Tilt is
up if you need auth or purchase delivery.

---

## 7. Live reload

| Change | What happens |
|---|---|
| Edit `cmd/**` or `internal/**` | Tilt restarts the Go server (~1 s) |
| Edit `frontend/src/**` | Vite HMR — instant browser update |
| Edit `frontend/package.json` | Tilt restarts the Vite dev server |
| Edit `docker-compose.dev.yml` | Tilt recreates the Navidrome container |
| Edit `docker/s6-rc.d/**` | Doesn't apply in dev mode (s6 only runs in the production image). Test in the production container. |

---

## 8. Common commands

```bash
# Run all tests
go test ./...

# Type-check + build (no run)
go build ./...

# Frontend type-check
cd frontend && npx tsc -b --noEmit

# Build the frontend into web/dist (embedded into the Go binary)
cd frontend && npm run build

# Verify the embed compiles (run from repo root after the npm build)
go build ./...

# Test endpoints (dev mode skips auth)
curl http://localhost:8088/api/health
curl http://localhost:8088/api/settings
```

---

## 9. Resetting

```bash
# Reset bridge-server state (keeps your music files)
rm -rf data/bridge data/navidrome

# Nuclear option (drops the music too)
rm -rf data/

# In production via Docker
docker compose down
docker volume rm bridge-data bridge-navidrome
```

After a full reset the next boot mints a new `server_id` and
`webhook_secret`. The user must re-pair from the marketplace UI for
delivery to work again.

---

## 10. Frontend layout

```
frontend/
├── src/
│   ├── main.tsx              app entry
│   ├── App.tsx               root + auth gate
│   ├── lib/
│   │   ├── supabase.ts       Supabase client (uses VITE_ env vars)
│   │   └── api.ts            apiFetch() — all /api calls go through here
│   ├── components/Layout.tsx sidebar + content shell
│   └── pages/
│       ├── Login.tsx         Supabase email/password
│       ├── Library.tsx       browse via proxied Subsonic API
│       ├── Purchases.tsx     history + redeliver
│       └── Settings.tsx      server status + config
├── vite.config.ts            dev proxy to :8888
└── index.html
```

Plain CSS — no Tailwind, no CSS-in-JS, no CSS modules. Two stylesheets:
`App.css` and `index.css`. Keep it that way for consistency.

`/api/config` returns the Supabase URL + anon key at runtime so the
frontend doesn't bake them into the JS bundle. This lets the same
binary work against any Supabase project.

---

## 11. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Tilt: ERROR: Local Supabase is not running` | Marketplace Tilt isn't up | Run `tilt up` in `~/OtherProjects/Bridge-Music-Marketplace` first |
| `:8088 already in use` | Stale dev server | `lsof -ti:8088 \| xargs kill -9` |
| Login fails: `invalid signing key` | `BRIDGE_SUPABASE_ANON_KEY` doesn't match the running Supabase | `supabase status` (in marketplace) → copy `anon key` into bridge-server's `.env.local` |
| `nd-credentials missing` after wipe | Container was killed mid-bootstrap | `rm -rf data/bridge` then `tilt up` again — first boot regenerates |
| Purchase webhook returns 401 | Local webhook secret rotated since pairing | Have the dev user re-run `/api/auto-pair` (or in dev: hardcode a fixed `BRIDGE_WEBHOOK_SECRET` in both repos' `.env.local`) |
| Navidrome scan fails: `no such file or directory: /run/s6-rc:s6-rc-init:.../servicedirs/navidrome/music` | s6 service didn't load container env, so Navidrome stored a relative `music` path in its DB | `docker volume rm bridge-navidrome` then restart. Production fix is in `docker/s6-rc.d/navidrome/run` (loads env explicitly). |
| Build error: `package github.com/.../X is not in std (...)` | Mismatched Go version | `go version` should report 1.25+. Update Go. |
| WSL2: builds slow | Code is on `/mnt/c/...` | Move under `~/...` |
