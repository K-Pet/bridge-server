# --- Stage 1: stock Navidrome binary ---------------------------------
FROM deluan/navidrome:0.61.1 AS navidrome

# --- Stage 2: build frontend ----------------------------------------
FROM node:22-alpine AS frontend
WORKDIR /src
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- Stage 3: build bridge-server ------------------------------------
# Track go.mod's `go 1.25.0` directive — older toolchains refuse to
# download modules when the project targets a newer Go version.
FROM golang:1.25-alpine AS sidecar
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /web/dist ./web/dist

# Bridge Music Supabase project + hCaptcha site key — baked into the
# binary at link time (NOT exported to ENV) so end-users running this
# image in Portainer / Docker don't see them listed as configurable
# knobs alongside their real config. None of these three values is a
# secret; they're public identifiers that already ship in the iOS app
# bundle and the Marketplace web client. Operators forking the image
# for a different Supabase project pass --build-arg at build time;
# runtime `-e BRIDGE_SUPABASE_URL=…` still wins over the ldflag.
ARG BRIDGE_SUPABASE_URL_DEFAULT=https://ryddlkjlpxtdrdvggipo.supabase.co
ARG BRIDGE_SUPABASE_ANON_KEY_DEFAULT=sb_publishable_Wl562UROunrQI0XIqlb8cQ_efLR70Oe
ARG BRIDGE_HCAPTCHA_SITE_KEY_DEFAULT=1dcef6cc-d22a-4f90-a882-c118b318f8f8

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w \
        -X github.com/bridgemusic/bridge-server/internal/config.BridgeSupabaseURL=${BRIDGE_SUPABASE_URL_DEFAULT} \
        -X github.com/bridgemusic/bridge-server/internal/config.BridgeSupabaseAnonKey=${BRIDGE_SUPABASE_ANON_KEY_DEFAULT} \
        -X github.com/bridgemusic/bridge-server/internal/config.BridgeHCaptchaSiteKey=${BRIDGE_HCAPTCHA_SITE_KEY_DEFAULT}" \
    -o /out/bridge-server ./cmd/bridge-server

# --- Stage 4: runtime ------------------------------------------------
FROM alpine:3.20

ARG S6_OVERLAY_VERSION=3.2.0.2

# Install s6-overlay
ADD https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz /tmp
ADD https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-x86_64.tar.xz /tmp
RUN tar -C / -Jxpf /tmp/s6-overlay-noarch.tar.xz \
 && tar -C / -Jxpf /tmp/s6-overlay-x86_64.tar.xz \
 && rm /tmp/s6-overlay-*.tar.xz

RUN apk add --no-cache ca-certificates ffmpeg tzdata wget

# Navidrome binary
COPY --from=navidrome /app/navidrome /app/navidrome

# Bridge server binary
COPY --from=sidecar /out/bridge-server /app/bridge-server

# s6 service definitions
COPY docker/s6-rc.d /etc/s6-overlay/s6-rc.d

# Navidrome config (localhost only, not exposed)
ENV ND_MUSICFOLDER=/data/music \
    ND_DATAFOLDER=/data/navidrome \
    ND_CONFIGFILE=/data/navidrome/navidrome.toml \
    ND_ADDRESS=127.0.0.1 \
    ND_PORT=4533

# Bridge server config — operational defaults (paths, ports).
ENV BRIDGE_PORT=8888 \
    BRIDGE_DATA=/data/bridge \
    BRIDGE_MUSIC_DIR=/data/music \
    BRIDGE_ND_URL=http://127.0.0.1:4533

# NOTE: Supabase URL / anon key / hCaptcha site key are linked into the
# bridge-server binary in stage 3, NOT set as ENV here. That keeps them
# out of `docker inspect` and Portainer's environment view, where they
# would otherwise look like user-configurable secrets.

VOLUME ["/data/music", "/data/navidrome", "/data/bridge"]
EXPOSE 8888

ENTRYPOINT ["/init"]
