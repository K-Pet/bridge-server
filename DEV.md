# Local Development

## Prerequisites

- [Go 1.23+](https://go.dev/dl/)
- [Node.js 20+](https://nodejs.org/)
- [Docker](https://docs.docker.com/get-docker/) (for Navidrome + Supabase)
- [Supabase CLI](https://supabase.com/docs/guides/cli) — local Supabase instance
- [Tilt](https://docs.tilt.dev/install.html) — orchestrates all services with live reload

Install on macOS:

```bash
brew install supabase/tap/supabase
curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
```

## Quick Start

```bash
# 1. Install frontend dependencies (first time only)
cd frontend && npm install && cd ..

# 2. Create local data directories
mkdir -p data/music data/navidrome data/bridge

# 3. Start local Supabase (auth, DB, storage)
supabase start --exclude logflare,vector

# 4. Seed the marketplace (first time only — creates catalog, test user, uploads audio)
supabase db reset
./supabase/seed.sh

# 5. Start everything
tilt up
```

This launches four services:

| Service | URL | What it does |
|---------|-----|-------------|
| **Supabase** | `http://localhost:54321` | Auth, DB, Storage (run separately) |
| **Supabase Studio** | `http://localhost:54323` | Database admin UI |
| **Navidrome** | `http://localhost:4533` | Music engine (Docker) |
| **Bridge Server** | `http://localhost:8080` | Go API backend |
| **Frontend** | `http://localhost:5173` | React dev server with HMR |

Open **http://localhost:5173** in your browser for the frontend.

The Tilt dashboard at **http://localhost:10350** shows logs and status for all services.

Press `Ctrl+C` or run `tilt down` to stop everything.

## How It Works

```
Browser (:5173)
    │
    ├── Static assets ──→ Vite dev server (HMR, fast refresh)
    │
    └── /api/*, /rest/*, /nd/*, /ws
            │
            └── proxy ──→ Bridge Server (:8080)
                              │
                              └── /rest/*, /nd/*
                                      │
                                      └── proxy ──→ Navidrome (:4533)
```

- **Vite** proxies all API calls to the Go backend (configured in `frontend/vite.config.ts`)
- **Bridge Server** runs in dev mode (`BRIDGE_DEV=true`), which:
  - Skips Supabase auth requirements (uses a fixed `dev-user` identity)
  - Gracefully handles Navidrome being unavailable
  - Disables the Supabase poller
- **Navidrome** runs as a stock Docker container with data persisted to `data/`

## Live Reload Behavior

| Change | What happens |
|--------|-------------|
| Edit `frontend/src/**` | Vite HMR — instant browser update, no reload |
| Edit `internal/**` or `cmd/**` | Tilt restarts the Go server automatically |
| Edit `frontend/package.json` | Tilt restarts the Vite dev server |
| Edit `docker-compose.dev.yml` | Tilt recreates the Navidrome container |

## Running Without Tilt

If you prefer to run services manually:

```bash
# Terminal 1: Navidrome
docker compose -f docker-compose.dev.yml up

# Terminal 2: Go backend
mkdir -p data/bridge data/music
BRIDGE_DEV=true \
BRIDGE_PORT=8080 \
BRIDGE_DATA=./data/bridge \
BRIDGE_MUSIC_DIR=./data/music \
BRIDGE_ND_URL=http://localhost:4533 \
go run ./cmd/bridge-server

# Terminal 3: Frontend
cd frontend && npm run dev
```

## Frontend Development

The frontend is a React + TypeScript app built with Vite, located in `frontend/`.

```
frontend/
├── src/
│   ├── main.tsx              # App entry point
│   ├── App.tsx               # Root component (auth + routing)
│   ├── lib/
│   │   ├── supabase.ts       # Supabase client (uses VITE_ env vars)
│   │   └── api.ts            # Bridge API client with auth headers
│   ├── components/
│   │   └── Layout.tsx        # Sidebar + content layout
│   └── pages/
│       ├── Login.tsx          # Supabase email/password login
│       ├── Library.tsx        # Browse library via Subsonic API
│       ├── Purchases.tsx      # Purchase history
│       └── Settings.tsx       # Server status and config
├── vite.config.ts            # Dev proxy + build config
└── index.html
```

### Local Supabase for Auth + Marketplace

The local Supabase instance handles auth, the marketplace catalog, and purchase storage.
Credentials are loaded from `.env.local` (gitignored) and injected by the Tiltfile.

Test login credentials (created by `./supabase/seed.sh`):
- **Email:** `test@bridge.music`
- **Password:** `testpass123`

The Go backend's `/api/config` endpoint serves the Supabase URL + anon key to the frontend
at runtime, so the frontend connects to local Supabase automatically.

### Building for Production

```bash
cd frontend && npm run build
```

This outputs to `web/dist/`, which is embedded into the Go binary via `go:embed`.

## Navidrome First Run

On first boot, Navidrome at `http://localhost:4533` will prompt you to create an admin user. The Bridge Server normally handles this automatically, but in dev mode you may want to create one manually to explore Navidrome's UI directly.

The Bridge Server creates its own admin (`bridge-admin`) when it connects to a fresh Navidrome instance. Credentials are stored in `data/bridge/nd-credentials`.

## Data Directories

All dev data is stored under `data/` (git-ignored):

| Path | Contents |
|------|----------|
| `data/music/` | Music library shared with Navidrome |
| `data/navidrome/` | Navidrome database and cache |
| `data/bridge/` | Bridge Server state (credentials, download queue) |

To reset everything: `rm -rf data/`

## Useful Commands

```bash
# Rebuild frontend and verify Go embed works
cd frontend && npm run build && cd .. && go build ./...

# Type-check frontend
cd frontend && npx tsc -b --noEmit

# Test the health endpoint
curl http://localhost:8080/api/health

# Test settings endpoint (dev mode, no auth needed)
curl http://localhost:8080/api/settings
```
