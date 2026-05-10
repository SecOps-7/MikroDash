# Contributing to MikroDash

Thanks for your interest in contributing.

## Before You Start

- Check [open issues](https://github.com/SecOps-7/MikroDash/issues) to avoid duplicating work
- For large changes, open an issue first to discuss the approach

## Development Setup

```sh
git clone https://github.com/SecOps-7/MikroDash.git
cd MikroDash
npm install
```

Run locally (requires a reachable RouterOS device):

```sh
node src/index.js
# or
npm start
```

Run tests:

```sh
node --test
```

## Project Conventions

- **No build step.** No TypeScript. No bundler. Plain Node.js CommonJS.
- **No new dependencies** without prior discussion — the dependency footprint is intentionally small.
- **Streaming-first**: prefer RouterOS `/listen` or `=interval=N` streams over polling.
- **No CDN references** — all frontend assets are self-hosted under `public/vendor/`.
- Collectors follow established patterns: inflight guard, idle-gating, dirty-check fingerprinting, `sanitizeErr()` before any error reaches the browser. Match these when adding a new collector.

## Submitting a Pull Request

1. Fork the repo and create a branch from `main`
2. Make your changes and ensure `node --test` passes
3. Keep commits focused — one logical change per commit
4. Open a PR with a clear description of what changed and why

## Reporting Bugs

Use the [bug report template](https://github.com/SecOps-7/MikroDash/issues/new?template=bug_report.yml). For security vulnerabilities, see [SECURITY.md](SECURITY.md).
