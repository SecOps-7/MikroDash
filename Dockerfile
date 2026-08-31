# syntax=docker/dockerfile:1
#
# MikroDash — the Go + TypeScript port, as a self-contained image.
#
# Built after cutover (2026-08-30), when the app was still being served by
# mounting the repo into `golang:1.25-alpine` and running the binary from it.
# That works and is exactly how the port was developed, but it is not a
# deployment artifact: it depends on the repo staying at one path on one host,
# and on a toolchain image that has no business being in production.

# ── the frontend ──────────────────────────────────────────────────────────
# ── NO NODE STAGE ANY MORE ─────────────────────────────────────────────────
#
# This was `FROM node:22-alpine`, `npm ci`, `npm run build` — 142 lines of Node
# driving esbuild, which is itself a Go program. `cmd/webbuild` does the same
# work from the Go stage below, and its output is BYTE-IDENTICAL: verified file
# by file against the Node build before the stage was removed.
#
# What this buys is not speed. It is that the image no longer pulls a second
# language runtime, and a contributor can produce a correct build with the Go
# toolchain alone. `tsc --noEmit` still needs Node and still lives in
# `web/package.json` — but a typecheck EMITS NOTHING, so it gates a change
# without being needed to make one.

# ── the geo database ──────────────────────────────────────────────────────
# DB-IP City Lite, fetched fresh at build. This replaced copying geoip-lite's
# `.dat` files out of the Node app's image, which tied the build to that image
# AND shipped whatever data geoip-lite 2.0.3 happened to publish — neither its
# Dockerfile nor its CI ever ran `updatedb`, so the addresses moved and the file
# did not.
#
# DB-IP is a direct download: no account, no licence key, no EULA. MaxMind
# GeoLite2 is more accurate and updates twice weekly, but needs a key that
# expires every 90 days, which would make this a build nobody else can run.
#
# THE MONTH IS TRIED, THEN THE ONE BEFORE. DB-IP publishes monthly and the new
# file appears partway through the first day; a build at 00:30 on the 1st would
# otherwise fail for a reason that has nothing to do with the build.
# --platform=$BUILDPLATFORM: an .mmdb is the same file on every architecture,
# so downloading it once natively beats downloading it three times, twice
# under emulation.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS geodata
RUN apk add --no-cache curl
RUN set -eux; \
    this=$(date -u +%Y-%m); \
    prev=$(date -u -d "$(date -u +%Y-%m-01) -1 day" +%Y-%m 2>/dev/null || echo "$this"); \
    for m in "$this" "$prev"; do \
      if curl -sSfL --retry 3 --max-time 600 \
           -o /dbip.mmdb.gz "https://download.db-ip.com/free/dbip-city-lite-$m.mmdb.gz"; then \
        echo "geo: fetched dbip-city-lite-$m"; break; \
      fi; \
    done; \
    test -s /dbip.mmdb.gz; \
    gunzip /dbip.mmdb.gz; \
    test -s /dbip.mmdb

# ── the binary ────────────────────────────────────────────────────────────
# CGO_ENABLED=0 is not decoration. `modernc.org/sqlite` is pure Go precisely so
# this binary needs no libc at runtime, which is what lets the final stage be a
# base image rather than a distro.
# --platform=$BUILDPLATFORM PINS THE TOOLCHAIN TO THE NATIVE BUILDER, and that is
# the single biggest thing keeping this build fast.
#
# Without it, buildx runs this whole stage once per target architecture, and two
# of the three under QEMU emulation. Measured on the v0.8.1 release build: 18m34s
# for three architectures, with the emulated arm/v7 `go build` alone taking over
# 280 seconds and `geogen` -- which walks ~14.7M networks -- far longer.
#
# Go cross-compiles from a single native toolchain, and CGO_ENABLED=0 means there
# is no C toolchain to arrange, so emulating anything here buys nothing. The
# binary is the ONLY per-architecture artefact; everything else this stage
# produces is identical across all three.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
# Supplied by buildx per target. GOARM wants the bare number, hence the ${..#v}.
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/mikrodash ./cmd/mikrodash

# THE FRONTEND, from the same stage and built for the BUILDER, not the target:
# JavaScript and JSON are the same bytes everywhere. `cmd/webbuild` composes index.html from the
# extracted markup and bundles the three entry documents through esbuild's Go
# API, at the version `web/package-lock.json` pins.
RUN go run ./cmd/webbuild -dir web

# THE GAZETTEER IS GENERATED HERE, not at runtime, and also for the builder. The picker behind
# /api/cities needs a LIST, and an .mmdb is a lookup structure: enumerating it
# means walking ~14.7M networks, which takes ~35s. `CityHolder` builds lazily on
# first search, so doing it at runtime would hang the first keystroke.
COPY --from=geodata /dbip.mmdb /geo/dbip-city-lite.mmdb
RUN go run ./cmd/geogen -mmdb /geo/dbip-city-lite.mmdb -out /geo/cities.json

# ── runtime ───────────────────────────────────────────────────────────────
FROM alpine:3.20
# ca-certificates: the notification transports talk TLS to Telegram, SMTP and
# ntfy, and an image with no roots fails all three at the moment they matter.
# tzdata: `alertTimestamp` calls time.LoadLocation with the install's display
# zone, which silently falls back to UTC without the database.
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/mikrodash /usr/local/bin/mikrodash
COPY --from=build /src/web/dist  /app/web/dist
COPY web/public                  /app/web/public
COPY --from=build /geo/dbip-city-lite.mmdb /app/geo/dbip-city-lite.mmdb
COPY --from=build /geo/cities.json            /app/geo/cities.json
VOLUME ["/data"]
EXPOSE 3081
ENTRYPOINT ["/usr/local/bin/mikrodash"]
# Standalone, because after cutover this process IS the app. Every one of these
# is overridable by giving the container its own arguments.
CMD ["-listen", ":3081", "-node=", "-data", "/data", \
     "-web", "/app/web/dist", "-static", "/app/web/public", \
     "-history", "-backup-scheduler", "-retention", "-alert-dispatch"]
