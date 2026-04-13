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
FROM golang:1.23-alpine AS sidecar
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" \
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

# Bridge server config
ENV BRIDGE_PORT=8080 \
    BRIDGE_DATA=/data/bridge \
    BRIDGE_MUSIC_DIR=/data/music \
    BRIDGE_ND_URL=http://127.0.0.1:4533

VOLUME ["/data/music", "/data/navidrome", "/data/bridge"]
EXPOSE 8080

ENTRYPOINT ["/init"]
