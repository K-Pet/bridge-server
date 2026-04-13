# Bridge Music Server — Local Dev Environment
#
# Orchestrates three services:
#   1. Navidrome (Docker) — music engine on :4533
#   2. Bridge Server (Go) — API backend on :8080
#   3. Vite Dev Server (Node) — frontend with HMR on :5173
#
# Usage: tilt up
# Dashboard: http://localhost:10350

# ─── 1. Navidrome via Docker Compose ──────────────────────────────────
docker_compose("docker-compose.dev.yml")
dc_resource("navidrome", labels=["backend"])

# ─── 2. Go Backend ───────────────────────────────────────────────────
local_resource(
    "bridge-server",
    serve_cmd=" ".join([
        "BRIDGE_DEV=true",
        "BRIDGE_PORT=8080",
        "BRIDGE_DATA=./data/bridge",
        "BRIDGE_MUSIC_DIR=./data/music",
        "BRIDGE_ND_URL=http://localhost:4533",
        "go", "run", "./cmd/bridge-server",
    ]),
    deps=[
        "cmd/",
        "internal/",
        "web/dist/",
    ],
    labels=["backend"],
    resource_deps=["navidrome"],
)

# ─── 3. Vite Frontend Dev Server ────────────────────────────────────
local_resource(
    "frontend",
    serve_cmd="npm run dev",
    serve_dir="frontend",
    deps=["frontend/package.json"],
    labels=["frontend"],
    links=["http://localhost:5173"],
)
