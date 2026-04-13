# Bridge Music Server — Local Dev Environment
#
# Orchestrates four services:
#   1. Navidrome (Docker) — music engine on :4533
#   2. Local Supabase — auth, DB, storage on :54321 (managed externally)
#   3. Bridge Server (Go) — API backend on :8080
#   4. Vite Dev Server (Node) — frontend with HMR on :5173
#
# Prerequisites:
#   supabase start --exclude logflare,vector
#   ./supabase/seed.sh   (first time only)
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
local_resource(
    "supabase",
    cmd="curl -sf http://127.0.0.1:54321/rest/v1/ > /dev/null && echo 'Supabase is running' || (echo 'ERROR: Local Supabase is not running. Run: supabase start --exclude logflare,vector' && exit 1)",
    labels=["backend"],
    allow_parallel=True,
)

# ─── 3. Go Backend ───────────────────────────────────────────────────
local_resource(
    "bridge-server",
    serve_cmd=" ".join([
        "BRIDGE_DEV=true",
        "BRIDGE_PORT=8080",
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
