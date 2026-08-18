# Contributing to MikroDash

Thanks for your interest in contributing. Small changes are as welcome as large ones — typo fixes, documentation, and a single-line bug fix all count.

## Before You Start

- Check [open issues](https://github.com/SecOps-7/MikroDash/issues) to avoid duplicating work
- [Good first issue](https://github.com/SecOps-7/MikroDash/labels/good%20first%20issue) is a reasonable place to start
- For large changes, open an issue first so we can agree on the approach before you spend time on it
- If something is unclear, ask in an issue — that is not a bother

## Development Setup

```sh
git clone https://github.com/SecOps-7/MikroDash.git
cd MikroDash
npm install
node patch-routeros.js
```

`patch-routeros.js` patches `node-routeros` (archived in 2024) for RouterOS 7.18+ compatibility. It runs once after install — the Docker build does the same thing. Skip it and live router connections misbehave in confusing ways.

Run it:

```sh
npm start
```

**You do not need a MikroTik router to contribute.** MikroDash starts without one and shows the setup wizard, so frontend, documentation and test work need nothing but Node. A reachable RouterOS device is only required to see live data.

Run the tests:

```sh
npm test
```

The tests use stubs throughout — no router, no network, no database setup. The suite takes a few seconds.

> `node --test` exits on its own. It used to need `--test-force-exit` because unstopped collectors
> kept the runner alive; that flag also truncated the tail of the largest file at random, so runs
> silently reported fewer tests than exist. Do not reintroduce it — if the suite hangs again, a
> test is leaking a timer and `test/helpers/collector-cleanup.js` is the place to look.

## Project Conventions

These are deliberate constraints rather than style preferences, and they are what keeps MikroDash a single `docker run` with no toolchain:

- **No build step.** Plain CommonJS, no TypeScript, no bundler — what you edit is what ships.
- **Self-hosted assets.** Everything the browser loads lives in `public/vendor/`, so the dashboard works on an isolated network with no internet access. No CDN references.
- **A small dependency footprint.** New dependencies are worth discussing first; often the thing you need is already there.
- **Streaming-first.** Prefer RouterOS `/listen` or `=interval=N` streams over polling, so the router does the work of noticing change rather than being asked repeatedly.
- **Errors are sanitised.** Anything reaching the browser goes through `sanitizeErr()` first.

Collectors follow established patterns — inflight guards, idle-gating, dirty-check fingerprinting. You do not need to know these before starting: **[AI_CONTEXT.md](AI_CONTEXT.md)** documents each one with examples, and copying the closest existing collector in `src/collectors/` is a perfectly good way to begin.

## Submitting a Pull Request

1. Fork the repo and create a branch from `main`
2. Make your changes and check `npm test` passes
3. Keep commits focused — one logical change per commit
4. Open a PR describing what changed and why

Do not worry about getting the conventions above exactly right first time. If something needs adjusting, that is what review is for, and it will be a conversation rather than a rejection.

## Reporting Bugs

Use the [bug report template](https://github.com/SecOps-7/MikroDash/issues/new?template=bug_report.yml). Router model and RouterOS version help a lot, since behaviour varies between versions.

For security vulnerabilities, please follow [SECURITY.md](SECURITY.md) instead of opening a public issue.
