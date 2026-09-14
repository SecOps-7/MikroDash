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

You need **Go 1.25+**. **Node 20+** is needed only to type-check and test the frontend: the frontend itself is built by a Go program, and nothing Node-related runs at runtime.

```sh
go run ./cmd/webbuild -dir web                    # build the TypeScript frontend into web/dist
go build ./cmd/mikrodash                          # the binary
./mikrodash -data ./devdata -web web/dist -static web/public
```

The dashboard is then at <http://localhost:3082> (`-listen` changes the address).

If you would rather not install a Go toolchain, the Go commands also run in a container:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.25-alpine sh -c "go vet ./... && go build ./..."
```

**You do not need a MikroTik router to contribute.** MikroDash starts without one and shows the setup wizard, so frontend, documentation and test work need nothing but the toolchain. A reachable RouterOS device is only required to see live data.

## Running the checks

```sh
(cd web && npm ci)   # once: TypeScript and esbuild for the frontend checks
sh tools/verify.sh   # everything: gofmt, vet, go test, generated code, tsc, web tests
```

`tools/verify.sh` is the one to run before opening a PR. Its Go half runs in a `golang` container, so it needs Docker; without Docker it says what it skipped, and `go vet ./... && go test ./...` covers the same ground with a local toolchain.

It **discovers** what to check rather than working from a list, so a new check runs without being registered anywhere:

| | |
|---|---|
| `internal/verify/` | 59 Go tests — static checks over the current source. Picked up by `go test ./...`. |
| `web/test/` | 40 test files that bundle the app's TypeScript and run it against a DOM shim, via `npm test` in `web/`. |

Package tests use the standard library `testing` package only.

## Project Conventions

These are deliberate constraints rather than style preferences:

- **Fewer router channels.** Concurrent API channels, not data volume or CPU, are what strain small hardware, so "more efficient" means asking the router for less. Each menu is read once however many collectors want it, and every collector supports both stream and poll delivery, chosen per router.
- **Every WebSocket event is declared with its payload type** (`hub.Declare`), and the browser's types are generated from those declarations by `cmd/tsgen`. A payload that is a Go map is typed in `web/src/events-hand.ts`, and `tsc` fails if that file and the declarations disagree about which events those are.
- **Generated code is never edited by hand.** `web/src/gen/` comes from `cmd/tsgen`, `cmd/pagesgen` and `tools/*-ts.js`; change the source and regenerate. The recordings under `testdata/` pin what the app does today, so change them deliberately, never by retyping one.
- **A check that cannot fail is worse than no check.** Anything that scans a set asserts it actually found something. An audit that silently measures zero reads exactly like one that passed.
- **A gap is recorded, never hidden.** The ledgers in `internal/verify/` fail in both directions: an unrecorded gap fails, and so does a recorded one that has since closed.
- **Self-hosted assets.** Everything the browser loads lives in `web/public/vendor/`, so the dashboard works on an isolated network with no internet access. No CDN references.
- **A small dependency footprint.** There are seven Go dependencies and each has a reason beyond convenience. `esbuild` is used through its Go API, which is why building the frontend needs no JavaScript runtime. New ones are worth discussing first.
- **Errors are sanitised.** Anything reaching the browser goes through `safe.Message()` first.
- **Deliberate changes are welcome; silent ones are not.** If your change alters what a page shows, a WebSocket payload or an interaction, say so in the PR. If it makes a check fail, update the check and explain why in the commit — do not delete it.

The collector layer — how data is read from the router, turned into payloads and sent to the pages that want it — is described in **[Collector-Architecture.md](Collector-Architecture.md)**. You do not need to read it before starting: copying the closest existing collector in `internal/collect/` is a perfectly good way to begin.

## Submitting a Pull Request

1. Fork the repo and create a branch from `main`
2. Make your changes and check `sh tools/verify.sh` passes
3. Keep commits focused — one logical change per commit
4. Open a PR describing what changed and why

Do not worry about getting the conventions above exactly right first time. If something needs adjusting, that is what review is for, and it will be a conversation rather than a rejection.

## Reporting Bugs

Use the [bug report template](https://github.com/SecOps-7/MikroDash/issues/new?template=bug_report.yml). Router model and RouterOS version help a lot, since behaviour varies between versions.

For security vulnerabilities, please follow [SECURITY.md](SECURITY.md) instead of opening a public issue.
