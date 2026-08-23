# Supports linux/amd64 and linux/arm64.
# node:24-alpine ships native layers for both platforms so no emulation is
# needed at runtime — only the CI build step uses QEMU for cross-compilation.
#
# linux/arm/v7 was dropped here: Node 24 dropped 32-bit ARM upstream, so
# node:24-alpine publishes amd64, arm64/v8 and s390x only. ARMv7 devices
# (RouterOS containers, 32-bit Pi builds) stay on 0.5.54, which is Node 20.
# TARGETPLATFORM is injected automatically by `docker buildx build --platform ...`
# and does not need to be declared or defaulted here.
#
# Node 24 rather than 20: geoip-lite 2.x declares `engines: { node: '>=24' }`,
# and npm only warns on an engine mismatch, so a future patch release using a
# Node 24 API would install cleanly, pass CI and then fail to load at runtime.
# Node 20 is also past the end of its LTS maintenance window. better-sqlite3
# compiles natively here, so its ABI rebuild is the thing to watch. See #101.
FROM node:24-alpine AS base
WORKDIR /app
# Build tools needed for better-sqlite3 native compilation on alpine
RUN apk add --no-cache python3 make g++
COPY package*.json ./
RUN npm install --omit=dev --no-audit --no-fund
# Patch node-routeros to handle RouterOS 7.18+ !empty API reply
COPY src/routeros/patchVerification.js ./src/routeros/patchVerification.js
COPY patch-routeros.js ./
RUN node patch-routeros.js
COPY . .

# ── test ─────────────────────────────────────────────────────────────────────
#
# The runtime image plus devDependencies, and nothing else. A test needing a
# dev-only tool — jsdom to drive a real renderer, espree to parse source — could
# not run at all before this stage existed: the runtime install is `--omit=dev`,
# so the module was simply absent and the whole file failed to load.
#
# test/ stays out of every image (see .dockerignore), so mount it at run time:
#
#   docker build --target test -t mikrodash-test .
#   docker run --rm -v "$PWD/test:/app/test:ro" mikrodash-test
#
# The second `npm install` re-resolves the whole tree and can restore an
# unpatched node-routeros over the patched one, so the patch is re-applied here.
# It is idempotent — already-applied markers are skipped — and since #113 it
# exits non-zero rather than leaving an unverified build behind.
FROM base AS test
RUN npm install --no-audit --no-fund \
 && node patch-routeros.js
# No --test-force-exit. CONTRIBUTING.md forbids it and explains why: it masks a
# test leaking a timer, and it truncates the tail of the largest file at random,
# so a run silently reports fewer tests than exist. If this hangs, a collector
# was constructed and never stopped — see test/helpers/collector-cleanup.js.
CMD ["node", "--test", "/app/test/*.test.js"]

# ── runtime ──────────────────────────────────────────────────────────────────
#
# Last on purpose. Docker builds the final stage when none is named, so
# `docker compose build` keeps producing the runtime image with no target and no
# compose change, and the dev dependencies above never reach it.
FROM base AS runtime
EXPOSE 3081
CMD ["node", "src/index.js"]
