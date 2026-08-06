# Supports linux/amd64, linux/arm64, and linux/arm/v7.
# node:24-alpine ships native layers for all three platforms so no emulation
# is needed at runtime — only the CI build step uses QEMU for cross-compilation.
# TARGETPLATFORM is injected automatically by `docker buildx build --platform ...`
# and does not need to be declared or defaulted here.
#
# Node 24 rather than 20: geoip-lite 2.x declares `engines: { node: '>=24' }`,
# and npm only warns on an engine mismatch, so a future patch release using a
# Node 24 API would install cleanly, pass CI and then fail to load at runtime.
# Node 20 is also past the end of its LTS maintenance window. better-sqlite3
# compiles natively here, so its ABI rebuild is the thing to watch. See #101.
FROM node:24-alpine
WORKDIR /app
# Build tools needed for better-sqlite3 native compilation on alpine
RUN apk add --no-cache python3 make g++
COPY package*.json ./
RUN npm install --omit=dev --no-audit --no-fund
# Patch node-routeros to handle RouterOS 7.18+ !empty API reply
COPY patch-routeros.js ./
RUN node patch-routeros.js
COPY . .
EXPOSE 3081
CMD ["node", "src/index.js"]
