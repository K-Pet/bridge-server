# Bridge Music Server

Self-hosted home server for the Bridge Music ecosystem. Wraps
[Navidrome](https://github.com/navidrome/navidrome) as an embedded
engine and adds:

- **Purchase delivery** — receives webhooks from the Bridge Music
  marketplace, downloads the audio files the user bought, and triggers
  a Navidrome scan.
- **Marketplace auth bridge** — verifies Supabase JWTs from the iOS app
  + web frontend; bootstraps Navidrome admin credentials on first run.
- **Branded frontend** — embedded React SPA at `/` so end users
  interact with Bridge Music, never with Navidrome's UI.

Ships as a single Docker image: `ghcr.io/k-pet/bridge-server:latest`.
One `docker run` and the user has a paired music server.

## How it fits in the ecosystem

```
            buys music                  delivers music
   user ────────────────► Marketplace ─────────────► Home Server
                                                    (this repo)
                              │                          │
                              ▼                          ▼
                          Supabase  ◄──── reads ────  iOS App
```

Three repos, one Supabase project:

| Repo | Role |
|---|---|
| [`Bridge-Music-Marketplace`](../Bridge-Music-Marketplace) | Storefront UI + Supabase schema + payment / delivery Edge Functions |
| **bridge-server** (this repo) | Self-hosted home server. Wraps Navidrome, receives delivery webhooks, downloads tracks |
| [`Bridge` (iOS)](../../Xcode%20Projects/Bridge) | SwiftUI player. Streams from this server via Subsonic; embeds the marketplace as a tab |

The marketplace owns the Supabase schema and Edge Functions; this repo
is a *consumer*. For the full ecosystem walkthrough see
[`../Bridge-Music-Marketplace/docs/ARCHITECTURE.md`](../Bridge-Music-Marketplace/docs/ARCHITECTURE.md).

## Get started

| If you want to… | Read |
|---|---|
| Set up the project locally | [`docs/DEVELOPMENT.md`](./docs/DEVELOPMENT.md) |
| Understand how this server works internally | [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) |
| Deploy a real bridge-server | [`docs/DEPLOYMENT.md`](./docs/DEPLOYMENT.md) |
| Look up the wire protocol with the marketplace | [`../Bridge-Music-Marketplace/docs/reference/PURCHASE_CONTRACT.md`](../Bridge-Music-Marketplace/docs/reference/PURCHASE_CONTRACT.md) |
| Add an HTTP endpoint or admin-UI page | [`../Bridge-Music-Marketplace/docs/CONTRIBUTING.md § 5`](../Bridge-Music-Marketplace/docs/CONTRIBUTING.md#5-recipes--bridge-server-home-server) |

## Tech stack

- **Go 1.25** (single binary, ~12 MB, statically linked)
- **Navidrome 0.61** (stock binary, embedded as `127.0.0.1:4533`)
- **s6-overlay** (process supervisor inside the container)
- **SQLite** (download queue, persisted to `/data/bridge`)
- **React 19 + TypeScript + Vite** (admin SPA, embedded via `go:embed`)
- **Docker** + multi-arch image (amd64 + arm64) via GitHub Actions
