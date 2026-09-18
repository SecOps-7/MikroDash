#!/bin/sh
# Every Go generator's `-check`, FOUND RATHER THAN LISTED: any cmd/*/main.go that
# declares a `check` flag. It prints the ones it ran and exits non-zero on the
# first stale table.
#
# One place for the list, because there were two -- the Docker block in
# tools/verify.sh named six by hand and its summary line named them again -- and
# CI ran neither: `verify.sh --no-docker` skipped the whole Go block, so a
# payload struct could change without web/src/gen/payloads.ts following and CI
# stayed green (review loop, item 11).
set -e
ran=''
for m in cmd/*/main.go; do
  grep -q 'flag.Bool("check"' "$m" || continue
  d=$(dirname "$m")
  go run "./$d" -check
  ran="$ran $(basename "$d")"
done
[ -n "$ran" ] || { echo 'gencheck: found no generator -- the discovery pattern no longer matches' >&2; exit 1; }
echo "generators current:$ran"
