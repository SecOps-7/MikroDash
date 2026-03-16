# Supports linux/amd64 and linux/arm64 (e.g. Raspberry Pi 4/5, R5S, Apple M-series).
# node:20-alpine ships native layers for both platforms so no emulation is
# needed at runtime — only the CI build step uses QEMU for cross-compilation.
# TARGETPLATFORM is injected automatically by `docker buildx build --platform ...`
# and does not need to be declared or defaulted here.
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev --no-audit --no-fund
# Patch node-routeros to handle RouterOS 7.18+ !empty API reply
COPY patch-routeros.js ./
RUN node patch-routeros.js
COPY . .
EXPOSE 3081
CMD ["node", "src/index.js"]
