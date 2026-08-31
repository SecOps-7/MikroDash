#!/bin/sh
# Everything this repo can check, DISCOVERED rather than listed.
#
# ---- WHY THIS EXISTS ------------------------------------------------------
#
# On 2026-08-25 `endpoint-audit` was found to have been failing for an unknown
# number of sessions. Nothing was wrong with the audit: every verification sweep
# had run a list of audit names typed out from memory, and that one was not on
# it. A hand-maintained list of what to check is a ledger like any other, and
# that one had no audit of its own.
#
# The same question asked of the OTHER categories found a bigger version of it:
# fifty-seven tools support `--check`, and a session could easily run none of
# them. A stale corpus is the quietest failure this project has — the gate
# consuming it still passes, against an implementation that has moved.
#
# So nothing here is named. Every category is a glob:
#
#   tools/*-check.js        the differential gates
#   tools/*audit*.js        the audits
#   tools/*.js --check      the generators, found by grepping for the flag
#   nodecheck/*.test.js     the fixture replays
#
# ---- THREE GENERATORS CANNOT RUN ON THE HOST ------------------------------
#
# `audit-cases`, `backup-history-cases` and `report-history-cases` require the
# live app's `better-sqlite3`, a native module present only in the `mikrodash`
# container. They are run there, and A SKIP IS REPORTED AS A SKIP: a generator
# that could not run is not a generator that passed.
#
# Copy the tree so the RELATIVE layout holds — `/work/tools` beside
# `/work/testdata`. Copying `tools` alone makes all three report their corpus
# stale, which cost a cycle the first time.
#
#   sh tools/verify.sh                     # works WITHOUT the reference
#   MIKRODASH_SRC=../MikroDash sh tools/verify.sh
#   MIKRODASH_SRC=../MikroDash sh tools/verify.sh --no-docker   # skip the three
#
# ---- THE REFERENCE IS OPTIONAL NOW, AND STILL WORTH HAVING ----------------
#
# Every gate and audit compares against a recording of the live implementation,
# committed under `testdata/`, so this sweep is meaningful on a fresh clone. With
# `MIKRODASH_SRC` set it does MORE: each recording is re-derived and compared
# against the source it claims to describe, which is the only thing that keeps a
# recording honest. The generator `--check` runs need it outright — they
# regenerate from the reference — and are reported as a skip when it is absent.
#
# ---- IT WAS TESTED THE WAY EVERYTHING ELSE HERE IS ------------------------
#
# A sweep that reports green is worth exactly as much as a gate that does, so
# this one was mutated too, on 2026-08-25:
#
#   * a storage key drifted     -> `audits: 18 run, 1 failed`, `verify: 1 failing`
#   * a corpus emptied          -> reported STALE, exit 1
#   * `--no-docker`             -> "SKIPPED by request, NOT checked: ..." and
#                                  "verify: green, but 3 generator(s) were NOT checked"
#
# The third is the one that mattered: a skip that printed nothing would make the
# container generators invisible again, which is the failure this file exists
# for.
#
# The GO section was mutated the same way when it was added:
#
#   * a gofmt violation   -> `FAIL go` / `gofmt: internal/hub/hub.go`, exit 1
#   * a failing test      -> `FAIL go` and the `--- FAIL: Test...` line
#   * a vet diagnostic    -> `FAIL go` and "format %d has arg ... of wrong type"
#   * `--no-docker`       -> "SKIPPED by request, NOT checked: gofmt, go vet,
#                            go test" and counted in the NOT-run total
#
# The vet case is worth naming: the first attempt appended a call to an
# unimported `fmt`, which is a BUILD error, not a vet one — it proved the section
# catches a broken build and said nothing about vet. Redone in a file that
# already imports `fmt`, so the diagnostic is genuinely vet's.
#
# ---- GO IS HERE NOW, AND THE OLD REASON WAS THE BUG ------------------------
#
# This file used to say: "Go is deliberately NOT here: it runs in a container
# with its own mount of the live repo, and CLAUDE.md documents that command on
# its own."
#
# That reason does not survive this file's own premise. `endpoint-audit` was red
# for an unknown number of sessions because a category was left to be remembered,
# and "documented in CLAUDE.md on its own" is exactly that. Nor does the container
# argument distinguish it: THREE generators already run here through `docker`.
#
# It was found the way the original was — by looking. `PORT-QUEUE.md` defines a
# green iteration as `gofmt -l .` silent, `go vet`, `go test ./...` with the live
# repo mounted read-only, AND the JS side; a whole session ran this sweep, called
# it green, and never once compiled the Go.
#
# The Go run is SLOW (about a minute; `internal/db` alone takes eleven seconds),
# so the module cache lives in a named docker volume rather than being downloaded
# every time. `--no-docker` skips it and SAYS SO, like the three generators.
set -u

cd "$(dirname "$0")/.." || exit 1
: "${MIKRODASH_SRC:=../MikroDash}"
export MIKRODASH_SRC

fail=0
skipped=0
note() { printf '%s\n' "$*"; }

# ── THE CENSUS: HOW MUCH EACH GATE ACTUALLY CHECKED ───────────────────────
#
# `run_group` captures each gate's output and, on success, THROWS IT AWAY. So
# "gates: 136 run, 0 failed" cannot tell a gate that compared 40 cases from one
# that compared none. That is this project's most expensive recurring bug in its
# own verification: `endpoint-audit` was red for an unknown number of sessions,
# and `hook-selftest` had been failing since 2026-08-23 — printing "17 of 32 hook
# cases wrong" and exiting 1 — with nobody watching.
#
# A gate going BLIND is quieter still, because it exits 0. So the sweep now
# records the largest number each gate prints, which is its corpus size, and
# fails when one DROPS. Growth ratchets the baseline up on its own; only a
# shrink needs a human, and re-baselining is one command.
#
# MEASURED BEFORE BEING TRUSTED (2026-08-30): 160 gates print a number, and two
# identical passes produced byte-identical counts — no timestamps, no ids, no
# run-to-run drift. A ratchet on an unstable number would be a gate that cries
# wolf until it is deleted.
CENSUS_FILE=testdata/gate-census.txt
CENSUS_TMP=$(mktemp)
CENSUS_FAILED=$(mktemp)
CENSUS_SKIPPED=$(mktemp)
trap 'rm -f "$CENSUS_TMP" "$CENSUS_FAILED" "$CENSUS_SKIPPED"' EXIT

run_group() {
  label=$1
  shift
  n=0
  bad=0
  for f in "$@"; do
    [ -e "$f" ] || continue
    n=$((n + 1))
    if ! out=$(node "$f" 2>&1); then
      bad=$((bad + 1))
      fail=$((fail + 1))
      note "  FAIL $f"
      printf '%s\n' "$out" | tail -3 | sed 's/^/        /'
      # ALREADY REPORTED, so the census stays quiet about it rather than
      # printing a second line about the same gate.
      printf '%s\n' "${f##*/}" >>"$CENSUS_FAILED"
    else
      # THE LARGEST NUMBER, not the last one. A summary reads "68 of 70 ... 0
      # ungated", and the corpus size is the big number while the zero is good
      # news — taking the last would ratchet on "0" and never fire.
      # A GATE THAT SAID IT SKIPPED is recorded as such, so the census does not
      # read its silence as a gate that stopped looking.
      if printf '%s' "$out" | grep -qE 'SKIP|Skipped\.'; then
        printf '%s\n' "${f##*/}" >>"$CENSUS_SKIPPED"
      fi
      cnt=$(printf '%s' "$out" | grep -oE '[0-9]+' | sort -rn | head -1)
      [ -n "$cnt" ] && printf '%s %s\n' "${f##*/}" "$cnt" >>"$CENSUS_TMP"
    fi
  done
  note "$label: $n run, $bad failed"
}

note '== gates =='
run_group 'gates' tools/*-check.js

note '== audits =='
# `audit-cases.js` and `audit-page-check.js` also match `*audit*`; they belong to
# the generator and gate groups, so they are excluded here rather than run twice.
audits=''
for f in tools/*audit*.js; do
  case "$f" in
  *-cases.js | *-check.js) continue ;;
  esac
  audits="$audits $f"
done
# `hook-selftest.js` matches NO group's glob — not `*audit*`, not `*-check.js`,
# and it has no `--check` — so this sweep never ran it. CLAUDE.md said to run it
# "after touching the hook", which is remembered rather than enforced, and that
# is precisely how `endpoint-audit` went red for an unknown number of sessions.
#
# It had in fact been failing since 2026-08-23, when the operator disabled the
# hook: it went on asserting the old behaviour, printed "17 of 32 hook cases
# wrong", exited 1, and nobody saw. It now asserts the CURRENT contract — the
# hook is a deliberate no-op — and joins the sweep so that claim is checked.
audits="$audits tools/hook-selftest.js"
# shellcheck disable=SC2086
run_group 'audits' $audits

note '== gate census =='
if [ ! -f "$CENSUS_FILE" ]; then
  sort "$CENSUS_TMP" >"$CENSUS_FILE"
  note "  baseline written: $(wc -l <"$CENSUS_FILE" | tr -d ' ') gates"
else
  cbad=0
  while read -r cname cwas; do
    [ -n "$cname" ] || continue
    # A gate that FAILED above is not also reported as blind.
    if grep -qxF "$cname" "$CENSUS_FAILED" 2>/dev/null; then continue; fi
    # NOR ONE THAT DECLARED A SKIP. `vacuity-audit` compares the two conditions
    # and needs BOTH, so without a reference it says so and prints no count —
    # not a gate that stopped looking, but one that could not run and said which.
    # The census reads counts, so a skip is indistinguishable from silence unless
    # the gate is recorded as having skipped at the point it ran.
    if grep -qxF "$cname" "$CENSUS_SKIPPED" 2>/dev/null; then continue; fi
    cnow=$(awk -v n="$cname" '$1 == n { print $2; exit }' "$CENSUS_TMP")
    if [ -z "$cnow" ]; then
      note "  FAIL $cname ran before and reports no count now — it is gone, renamed, or has stopped saying what it checked"
      cbad=$((cbad + 1))
      fail=$((fail + 1))
    elif [ "$cnow" -lt "$cwas" ]; then
      note "  FAIL $cname checked $cnow, was $cwas — a gate that shrank is a gate that stopped looking at something"
      cbad=$((cbad + 1))
      fail=$((fail + 1))
    fi
  done <"$CENSUS_FILE"
  if [ "$cbad" -eq 0 ]; then
    # RATCHET, never lower: growth is recorded silently, so the floor only ever
    # rises and a later shrink is measured against the high-water mark.
    awk '{ if ($2 > m[$1] || !($1 in m)) m[$1] = $2 } END { for (k in m) print k, m[k] }' \
      "$CENSUS_FILE" "$CENSUS_TMP" | sort >"$CENSUS_FILE.new" && mv "$CENSUS_FILE.new" "$CENSUS_FILE"
  fi
  note "gate census: $(wc -l <"$CENSUS_FILE" | tr -d ' ') gates tracked, $cbad shrunk"
  if [ "$cbad" -gt 0 ]; then
    note "  to re-baseline deliberately: rm $CENSUS_FILE && sh tools/verify.sh"
  fi
fi

# HOISTED above the skip branch below. The container-generator section AFTER the
# `fi` reads both of these, so defining them inside the else made that section die
# with "DOCKER_ONLY: parameter not set" whenever the skip fired — a section that
# silently stopped running, which is the failure this file exists to prevent.
DOCKER_ONLY='audit-cases backup-history-cases report-history-cases pdf-metrics-cases pdf-render-cases report-build-cases report-builders-cases sendnow-cases wifiscan-admit-cases wifiscan-accum-cases alertfeed-cases alertwrite-cases'
# COUNTED, never typed. This said "3" in three places and a fourth generator
# joining the list on 2026-08-25 would have made every one of them a lie -- the
# skip tally, the summary line, and therefore the operator's idea of how much the
# sweep actually checked.
DOCKER_ONLY_N=$(printf '%s\n' $DOCKER_ONLY | wc -l | tr -d ' ')

note '== generators (--check) =='
# ── A GENERATOR'S `--check` NEEDS THE REFERENCE, BY CONSTRUCTION ──────────
#
# `--check` asks "is the committed corpus still what this generator would
# produce?" Measured: 67 of the generators RUN the reference implementation and
# 36 read it. Without one there is nothing to regenerate FROM, so the question
# has no answer — every generator reports stale, and the number means nothing.
#
# That is not the corpora going bad. A corpus is committed and the gates consume
# it directly; it is already the frozen artefact. What disappears is the ability
# to re-derive it, which is a property of the reference being gone rather than of
# anything here being wrong.
#
# SO IT IS REPORTED AS A SKIP, and says so — the same rule this file applies to
# `--no-docker` and the container generators. A skip that printed nothing, or a
# hundred meaningless failures, would both make the summary line a lie.
# THE FILE, NOT THE DIRECTORY, and they are not the same question after cutover.
#
# This tested `-d "$MIKRODASH_SRC"` until 2026-08-31. `lift.js`'s referenceAbsent()
# has always tested for `public/app.js` inside it, and the two agreed only while
# the reference was a separate repository. Once the port landed HERE, the default
# `../MikroDash` resolves to THIS repository -- a directory that certainly exists
# and contains no Node source -- so the skip never fired and all 105 generators
# ran and crashed with MODULE_NOT_FOUND.
#
# One question, one definition. A reference is present when public/app.js is.
if [ ! -f "$MIKRODASH_SRC/public/app.js" ]; then
  GEN_N=$(ls tools/*-cases.js tools/*-tables.js 2>/dev/null | wc -l | tr -d ' ')
  note "  SKIPPED, NOT checked: all $GEN_N generator --check runs need the reference at"
  note "  $MIKRODASH_SRC to regenerate from. The committed corpora are unaffected — the"
  note "  gates read them directly — but whether they could still be re-derived is a"
  note "  question that no longer has an answer."
  skipped=$((skipped + GEN_N))
else
n=0
bad=0
# Generators that CANNOT run on the host: they need a native module from the live
# app's node_modules, which exists only inside the `mikrodash` container.
# `audit-cases`, `backup-history-cases` and `report-history-cases` want
# `better-sqlite3`; `pdf-metrics-cases` and `pdf-render-cases` want `pdfkit`.
# `sendnow-cases` needs no native module either, but `scheduler.js` requires
# `pdf.js` which requires pdfkit — requiring a module runs its whole tree, so it
# cannot even be loaded on the host.
# `report-build-cases` needs no native module, but it records what
# `toLocaleString()` does -- which is a property of the RUNTIME's locale. The
# number the live app prints is the one its own container produces, so that is
# where the corpus is generated and checked.
for f in tools/*.js; do
  grep -q "includes('--check')" "$f" || continue
  base=$(basename "$f" .js)
  case " $DOCKER_ONLY " in
  *" $base "*) continue ;;
  esac
  n=$((n + 1))
  if ! out=$(node "$f" --check 2>&1); then
    bad=$((bad + 1))
    fail=$((fail + 1))
    note "  STALE $f"
    printf '%s\n' "$out" | tail -2 | sed 's/^/        /'
  fi
done
note "generators: $n checked, $bad stale"
fi

note '== generators needing the container =='
if [ "${1:-}" = '--no-docker' ]; then
  note "  SKIPPED by request, NOT checked: $DOCKER_ONLY"
  skipped=$((skipped + DOCKER_ONLY_N))
elif docker exec mikrodash true 2>/dev/null; then
  # THE CONTAINER IS RUNNING: exec into it, which is cheapest.
  docker exec mikrodash rm -rf /work >/dev/null 2>&1
  docker exec mikrodash mkdir -p /work >/dev/null 2>&1
  docker cp tools mikrodash:/work/tools >/dev/null 2>&1
  docker cp testdata mikrodash:/work/testdata >/dev/null 2>&1
  bad=0
  for base in $DOCKER_ONLY; do
    if ! out=$(docker exec -e MIKRODASH_SRC=/app mikrodash node "/work/tools/$base.js" --check 2>&1); then
      bad=$((bad + 1))
      fail=$((fail + 1))
      note "  STALE tools/$base.js"
      printf '%s\n' "$out" | tail -2 | sed 's/^/        /'
    fi
  done
  docker exec mikrodash rm -rf /work >/dev/null 2>&1
  note "container generators: $DOCKER_ONLY_N checked, $bad stale"
elif docker image inspect mikrodash >/dev/null 2>&1; then
  # ── AND AFTER CUTOVER, FROM THE IMAGE ─────────────────────────────────────
  #
  # These twelve need `better-sqlite3` or `pdfkit`, which exist only in the live
  # app's node_modules. That used to mean "the mikrodash CONTAINER must be up" —
  # and at cutover on 2026-08-30 it was stopped for good, which turned twelve
  # gates into a permanent SKIP in one step. The sweep still said green, with the
  # skips in a line above it that would be read once and then never again.
  #
  # The dependency was never the running container: it is the IMAGE's
  # node_modules. A throwaway `docker run` has them just as much, needs no port,
  # and cannot disturb the app now serving on 3081. The repo is mounted rather
  # than copied in, so there is nothing to clean up afterwards either.
  bad=0
  for base in $DOCKER_ONLY; do
    if ! out=$(docker run --rm -e MIKRODASH_SRC=/app \
      -v "$PWD/tools":/work/tools:ro -v "$PWD/testdata":/work/testdata:ro \
      mikrodash node "/work/tools/$base.js" --check 2>&1); then
      bad=$((bad + 1))
      fail=$((fail + 1))
      note "  STALE tools/$base.js"
      printf '%s\n' "$out" | tail -2 | sed 's/^/        /'
    fi
  done
  note "container generators: $DOCKER_ONLY_N checked from the image, $bad stale"
else
  note '  SKIPPED — no mikrodash container OR image, so these were NOT checked:'
  note "    $DOCKER_ONLY"
  skipped=$((skipped + DOCKER_ONLY_N))
fi

note '== nodecheck =='
if out=$(node --test nodecheck/*.test.js 2>&1); then
  printf '%s\n' "$out" | grep -E '^. (pass|fail)' | sed 's/^/  /'
else
  fail=$((fail + 1))
  note '  FAIL nodecheck'
  printf '%s\n' "$out" | grep -E '^not ok' | head -5 | sed 's/^/        /'
fi

note '== go =='
# DISCOVERED, not listed: no `go.mod`, no Go to check.
if [ ! -f go.mod ]; then
  note '  no go.mod — nothing to check'
elif [ "${1:-}" = '--no-docker' ]; then
  skipped=$((skipped + 1))
  note '  SKIPPED by request, NOT checked: gofmt, go vet, go test'
elif ! command -v docker >/dev/null 2>&1; then
  skipped=$((skipped + 1))
  note '  NO DOCKER, NOT checked: gofmt, go vet, go test'
else
  # The live repo is mounted READ-ONLY at /live. Without it the proplist drift
  # test SKIPS, and a gate that never runs is not a gate.
  live=$(cd "$MIKRODASH_SRC" 2>/dev/null && pwd)
  if [ -z "$live" ]; then
    skipped=$((skipped + 1))
    note "  MIKRODASH_SRC ($MIKRODASH_SRC) does not resolve, NOT checked"
  else
    out=$(docker run --rm \
      -v "$PWD":/src -w /src \
      -v "$live":/live:ro \
      -v mikrodash-gomod:/go/pkg/mod \
      -e MIKRODASH_SRC=/live \
      golang:1.25-alpine \
      sh -c 'set -e
        unformatted=$(gofmt -l .)
        if [ -n "$unformatted" ]; then echo "gofmt: $unformatted"; exit 1; fi
        go vet ./...
        go test ./...' 2>&1)
    if [ $? -eq 0 ]; then
      note "  gofmt, vet, test ok ($(printf '%s\n' "$out" | grep -c '^ok') package(s))"
    else
      fail=$((fail + 1))
      note '  FAIL go'
      printf '%s\n' "$out" | grep -E 'gofmt:|^(FAIL|---|# )|\.go:' | head -6 | sed 's/^/        /'
    fi
  fi
fi

note '== typescript =='
if (cd web && npx tsc --noEmit >/dev/null 2>&1); then
  note '  tsc ok'
else
  fail=$((fail + 1))
  note '  FAIL tsc'
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
