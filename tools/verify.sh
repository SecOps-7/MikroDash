#!/bin/sh
# EVERYTHING THIS REPOSITORY CAN CHECK. Run it before calling a session green.
#
# ── WHAT HAPPENED TO THE OTHER 900 LINES ────────────────────────────────────
#
# This script used to drive 136 gates, 35 audits, 105 corpus generators and a
# census over all of them. Nearly all of that asked ONE question: does this app
# still reproduce a frozen recording of the Node implementation it replaced? That
# question was retired with the port -- this is the product now, not a
# reproduction of one -- and the recordings went with it on 2026-09-01.
#
# The checks that asked a DIFFERENT question moved rather than died. They read the
# CURRENT source and assert properties that are still worth holding:
#
#   internal/verify/   Go tests -- credentials, identity columns, the
#                      WebSocket vocabulary, endpoints, selectors, reachability,
#                      the blur-suspend guard and more (CLAUDE.md counts them,
#                      and a test re-measures that count).
#   web/test/          tests that bundle the app's own TypeScript and run it
#                      against a DOM shim. JavaScript-hosted because testing
#                      TypeScript needs a JavaScript runtime and Go has no DOM.
#
# ── THE CENSUS IS GONE, AND THE PROPERTY IT PROTECTED IS NOT ────────────────
#
# `testdata/gate-census.txt` recorded how much each gate checked and failed when
# one shrank. It existed because `run_group` threw away a passing gate's output,
# so "136 run, 0 failed" could not tell a gate that compared forty cases from one
# that compared none. `go test` and `node --test` do not have that problem: a test
# that stops asserting still reports itself, and a package that stops building
# fails loudly.
#
# ── "DISCOVER, DON'T LIST" SURVIVES, AND THAT MATTERS MOST ──────────────────
#
# The old script globbed `tools/*-check.js` so a category could not be forgotten
# -- `endpoint-audit` was red for an unknown number of sessions because sweeps ran
# a list of names typed from memory. Nothing here is listed either: `go test ./...`
# finds every new test under `internal/verify/`, and `web/test/run.mjs` globs
# `*.test.ts`. Adding a check requires no edit to this file, which is the whole
# point.
set -u

fail=0
skipped=0
note() { printf '%s\n' "$*"; }

# ── Go: format, vet, tests, and the generators' -checks ────────────────────
#
# `go test ./...` covers internal/verify automatically -- see the note above.
#
# THE GO GENERATORS' `-check`s run from tools/gencheck.sh, which finds them. They
# read `internal/`, so they check on any clone with nothing mounted: `tsgen` fails
# when a payload struct changes without `web/src/gen/payloads.ts` following, and
# so on for each. Under --no-docker they run on the host's Go when there is one,
# which is how CI (Go installed, no container) checks them at all.
note '== go =='
if [ ! -f go.mod ]; then
  note '  no go.mod — nothing to check'
elif [ "${1:-}" = '--no-docker' ] || ! command -v docker >/dev/null 2>&1; then
  skipped=$((skipped + 1))
  note '  NOT checked here: gofmt, go vet, go test (--no-docker, or no docker)'
  if command -v go >/dev/null 2>&1; then
    if out=$(sh tools/gencheck.sh 2>&1); then
      note "  $(printf '%s\n' "$out" | tail -1)"
    else
      fail=$((fail + 1))
      note '  FAIL generated code is stale'
      printf '%s\n' "$out" | tail -4 | sed 's/^/        /'
    fi
  else
    skipped=$((skipped + 1))
    note '  NO GO, NOT checked: the generators (tools/gencheck.sh)'
  fi
else
  # ── THE BUILD CACHE IS MOUNTED, AND IT IS THE DIFFERENCE BETWEEN 110s AND 1s
  #
  # `mikrodash-gomod` has been here since the start and caches the DOWNLOADS. It
  # does nothing for compilation: a fresh container has an empty
  # /root/.cache/go-build, so every run rebuilt the whole tree and the test half
  # of this sweep took a minute and three quarters. With the cache volume, a run
  # that changed one file finishes in about a second.
  #
  # LIKE `mikrodash-gomod`, THIS VOLUME LOOKS UNUSED BETWEEN RUNS. CLAUDE.md's
  # standing rule covers it: never `docker volume prune` on this daemon.
  out=$(docker run --rm \
    -v "$PWD":/src -w /src \
    -v mikrodash-gomod:/go/pkg/mod \
    -v mikrodash-gocache:/root/.cache/go-build \
    golang:1.25-alpine \
    sh -c 'set -e
      unformatted=$(gofmt -l .)
      if [ -n "$unformatted" ]; then echo "gofmt: $unformatted"; exit 1; fi
      go vet ./...
      go test ./...
      sh tools/gencheck.sh' 2>&1)
  if [ $? -eq 0 ]; then
    note "  gofmt, vet, test ok ($(printf '%s\n' "$out" | grep -c '^ok') package(s)); $(printf '%s\n' "$out" | tail -1)"
  else
    fail=$((fail + 1))
    note '  FAIL go'
    printf '%s\n' "$out" | grep -E 'gofmt:|STALE|^Run:|^(FAIL|---|# )|\.go:' | head -8 | sed 's/^/        /'
  fi
fi

# ── TypeScript: the type checker over everything that ships ────────────────
note '== typescript =='
if (cd web && npx tsc --noEmit >/dev/null 2>&1); then
  note '  tsc ok'
else
  fail=$((fail + 1))
  note '  FAIL tsc'
  (cd web && npx tsc --noEmit 2>&1 | head -6 | sed 's/^/        /')
fi

# -- The JavaScript generators: every tools/*-ts.js, found rather than listed --
#
# Each writes a web/src/gen/ table from JSON and takes --check to fail when that
# table is stale. None ran here, so a JSON edit without a regeneration shipped a
# stale table and nothing failed. Globbed, like everything else in this file, so
# a new generator is checked without an edit here.
note '== generated tables =='
gen_count=0
gen_stale=''
for g in tools/*-ts.js; do
  [ -f "$g" ] || continue
  gen_count=$((gen_count + 1))
  node "$g" --check >/dev/null 2>&1 || gen_stale="$gen_stale $(basename "$g")"
done
if [ -z "$gen_stale" ]; then
  note "  $gen_count generator(s) current"
else
  fail=$((fail + 1))
  note "  FAIL stale:$gen_stale (run each with node, without --check, to regenerate)"
fi

# ── The frontend's own tests ───────────────────────────────────────────────
note '== web tests =='
if [ ! -f web/package.json ]; then
  note '  no web/package.json — nothing to run'
elif out=$( (cd web && npm test --silent) 2>&1 ); then
  note "  $(printf '%s\n' "$out" | grep -aoE '(tests|pass|fail) [0-9]+' | tr '\n' ' ')"
else
  fail=$((fail + 1))
  note '  FAIL web tests'
  printf '%s\n' "$out" | grep -aE '^(not ok|✖|  Error|AssertionError)' | head -8 | sed 's/^/        /'
fi

note ''
if [ "$fail" -ne 0 ]; then
  note "verify: $fail failing"
  exit 1
fi
if [ "$skipped" -ne 0 ]; then
  note "verify: green, but $skipped check(s) were NOT run — see above"
  exit 0
fi
note 'verify: green'
