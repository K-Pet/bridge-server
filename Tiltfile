# Bridge Music Server — Local Dev Environment
#
# Orchestrates four services:
#   1. Navidrome (Docker) — music engine on :4533
#   2. Supabase health check — verifies the marketplace-managed Supabase
#      stack is reachable on :54321 (this repo no longer owns the schema)
#   3. Bridge Server (Go) — API backend on :8088
#   4. Vite Dev Server (Node) — legacy internal frontend on :5173
#
# Prerequisites (run in ~/OtherProjects/Bridge-Music-Marketplace first):
#   tilt up       # which starts supabase + applies migrations + seeds
#   — OR —
#   supabase start --exclude logflare,vector    (from the marketplace repo)
#
# Usage: tilt up
# Dashboard: http://localhost:10350

# ─── Load .env.local for Supabase credentials ────────────────────────
def read_env_file(path):
    """Parse a .env file into a dict, skipping comments and blank lines."""
    env = {}
    for line in str(read_file(path)).splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            key, _, value = line.partition("=")
            env[key.strip()] = value.strip()
    return env

env = read_env_file(".env.local")

# ─── 1. Navidrome via Docker Compose ──────────────────────────────────
docker_compose("docker-compose.dev.yml")
dc_resource("navidrome", labels=["backend"])

# ─── 2. Supabase health check ────────────────────────────────────────
# The marketplace repo now owns the Supabase stack (schema + Edge
# Functions).  We only verify here that :54321 is reachable.
local_resource(
    "supabase",
    cmd="curl -sf http://127.0.0.1:54321/rest/v1/ > /dev/null && echo 'Supabase reachable' || (echo 'ERROR: Local Supabase is not running. Bring it up from ~/OtherProjects/Bridge-Music-Marketplace (tilt up, or supabase start).' && exit 1)",
    labels=["backend"],
    allow_parallel=True,
)

# ─── 3. Go Backend ───────────────────────────────────────────────────
local_resource(
    "bridge-server",
    serve_cmd=" ".join([
        "BRIDGE_DEV=true",
        "BRIDGE_PORT=8088",
        "BRIDGE_DATA=./data/bridge",
        "BRIDGE_MUSIC_DIR=./data/music",
        "BRIDGE_ND_URL=http://localhost:4533",
        "BRIDGE_SUPABASE_URL=" + env.get("BRIDGE_SUPABASE_URL", ""),
        "BRIDGE_SUPABASE_ANON_KEY=" + env.get("BRIDGE_SUPABASE_ANON_KEY", ""),
        "BRIDGE_SUPABASE_SERVICE_KEY=" + env.get("BRIDGE_SUPABASE_SERVICE_KEY", ""),
        "BRIDGE_SUPABASE_JWT_SECRET=" + env.get("BRIDGE_SUPABASE_JWT_SECRET", ""),
        "BRIDGE_WEBHOOK_SECRET=" + env.get("BRIDGE_WEBHOOK_SECRET", ""),
        "BRIDGE_DELIVERY_MODE=" + env.get("BRIDGE_DELIVERY_MODE", "poll"),
        "BRIDGE_POLL_INTERVAL=" + env.get("BRIDGE_POLL_INTERVAL", "30s"),
        # URL the embedded SPA iframes for the Storefront tab. In dev this is
        # the Expo web metro server (see Bridge-Music-Marketplace/Tiltfile).
        "BRIDGE_MARKETPLACE_URL=" + env.get("BRIDGE_MARKETPLACE_URL", "http://localhost:8081"),
        "go", "run", "./cmd/bridge-server",
    ]),
    deps=[
        "cmd/",
        "internal/",
        "web/dist/",
    ],
    labels=["backend"],
    resource_deps=["navidrome", "supabase"],
)

# ─── 4. Vite Frontend Dev Server ────────────────────────────────────
local_resource(
    "frontend",
    serve_cmd="npm run dev",
    serve_dir="frontend",
    deps=["frontend/package.json"],
    labels=["frontend"],
    links=["http://localhost:5173"],
)
