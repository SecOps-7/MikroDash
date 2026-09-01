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
```

You need **Go 1.25+** and **Node 20+**. Node is for the frontend build only; nothing Node-related runs at runtime.

```sh
go run ./cmd/webbuild -dir web                    # the TypeScript frontend
go build ./cmd/mikrodash                          # the binary
./mikrodash -data ./devdata -web web/dist -static web/public
```

If you would rather not install a Go toolchain, everything below also runs in a container:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.25-alpine sh -c "go vet ./... && go build ./..."
```

**You do not need a MikroTik router to contribute.** MikroDash starts without one and shows the setup wizard, so frontend, documentation and test work need nothing but the toolchain. A reachable RouterOS device is only required to see live data.

## Running the checks

```sh
go test ./...        # unit tests and the differential gates
sh tools/verify.sh   # everything: Go (gofmt, vet, tests, tsgen), tsc, web tests
```

`tools/verify.sh` is the one to run before opening a PR. It **discovers** what to check rather than working from a list, so a category cannot be forgotten — that rule exists because an audit once sat red for an unknown number of sessions while every sweep ran a list of names typed from memory.

**You do not need the original Node implementation, and there is nothing to point
at.** The verification harness that compared this app against a recording of it
was retired on 2026-09-01. What runs now reads only this repository:

| | |
|---|---|
| `internal/verify/` | 23 Go tests — static checks over the current source. Picked up by `go test ./...`; nothing lists them. |
| `web/test/` | 14 test files that bundle the app's TypeScript and run it against a DOM shim, via `npm test`. |

Both are discovered by glob, so adding a check needs no edit to `verify.sh`.



With the reference present each recording is **re-derived and compared against it**, which is the only thing that stops a recording drifting from the source it claims to describe. The generator `--check` runs also need it — they regenerate their corpora from the reference — and the sweep reports them as a skip, saying so, when it is absent.

## Project Conventions

These are deliberate constraints rather than style preferences:

- **Gates are generated, never transcribed.** `tools/*-cases.js` built their corpora by RUNNING or LIFTING the Node implementation. That source was removed at the cutover, so those corpora are now frozen artefacts and their `--check` runs report a skip. A table retyped by hand is a fork with no update path — if one needs re-deriving, check out `v0.7.40` rather than editing the corpus.
- **A check that cannot fail is worse than no check.** Anything that scans a set asserts it actually found something. An audit that silently measures zero reads exactly like one that passed.
- **Self-hosted assets.** Everything the browser loads lives in `web/public/vendor/`, so the dashboard works on an isolated network with no internet access. No CDN references.
- **A small dependency footprint.** There are seven Go dependencies and each has a reason beyond convenience — the newest, `esbuild`, is there because it *removed* a runtime: the frontend build no longer needs Node. New ones are worth discussing first.
- **Streaming-first.** Prefer RouterOS `/listen` or `=interval=N` streams over polling, so the router does the work of noticing change rather than being asked repeatedly. Concurrent API channels, not data volume, are what strain small hardware.
- **Errors are sanitised.** Anything reaching the browser goes through `safe.Message()` first.
- **Nothing user-visible changes by accident.** The gates compare the rendered page against a recording of how it looked at cutover, so a change to markup or interaction will fail one. That is the point: a deliberate change is welcome and needs the gate re-aimed and the reason written down; an unnoticed one is a bug. The recording cannot be regenerated, so re-aiming is the only route -- deleting the gate is not.

Collectors follow established patterns — inflight guards, idle-gating, dirty-check fingerprinting. You do not need to know these before starting: **[AI_CONTEXT.md](AI_CONTEXT.md)** documents each one with examples, and copying the closest existing collector in `internal/collect/` is a perfectly good way to begin.

## Submitting a Pull Request

1. Fork the repo and create a branch from `main`
2. Make your changes and check `sh tools/verify.sh` passes
3. Keep commits focused — one logical change per commit
4. Open a PR describing what changed and why

Do not worry about getting the conventions above exactly right first time. If something needs adjusting, that is what review is for, and it will be a conversation rather than a rejection.

## Reporting Bugs

Use the [bug report template](https://github.com/SecOps-7/MikroDash/issues/new?template=bug_report.yml). Router model and RouterOS version help a lot, since behaviour varies between versions.

For security vulnerabilities, please follow [SECURITY.md](SECURITY.md) instead of opening a public issue.
