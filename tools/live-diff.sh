#!/bin/sh
# Ask the Go server and the Node app the SAME question and diff the answers.
#
# ── WHY THIS IS NOT IN verify.sh ────────────────────────────────────────────
#
# It needs a running Node app, the live /data, and a dashboard login. A gate may
# assume none of those, and one that silently passed when they were absent would
# be worse than no gate — which is exactly what happened to `/api/cities` (see
# the geoip note below). So this is run BY HAND, and what it finds gets written
# down.
#
# ── WHAT IT HAS FOUND ───────────────────────────────────────────────────────
#
# Two defects in two runs, neither findable by any test in this repo, because a
# round trip through one implementation agrees with itself whatever it wrote:
#
#   2026-08-28  /api/sites sent ""   where the live app sends null
#   2026-08-28  /api/routers sent 11 fields of 23
#
# ── USAGE ───────────────────────────────────────────────────────────────────
#
#   MDU=<user> MDP=<password> sh tools/live-diff.sh
#
# The credentials come from the ENVIRONMENT and are never written anywhere. They
# are a dashboard login for the live app; there is no way to derive one, because
# users.json holds scrypt hashes. Ask the operator.
#
# ── THE GEOIP MOUNT IS NOT OPTIONAL ─────────────────────────────────────────
#
# ── `-no-pool` IS MANDATORY HERE, ADDED 2026-08-29 ──────────────────────────
#
# This starts a THIRD process against the same fleet, and without the flag it is
# standalone — `server.go` builds the background pool from `srv.standalone &&
# !opts.NoPool`, so it would open and HOLD a connection to every enabled router
# while the live app is already watching them. That is the arrangement
# `PORT-QUEUE.md` blocker 2 exists to prevent, and it was measured happening for
# most of 2026-08-29: established sockets to 10.0.0.4:8728 and 10.0.0.53:8729
# that nobody had a page open on.
#
# This script is a DIFF of HTTP payloads. It needs no collectors and no pool at
# all — every endpoint it asks reads `/data` or the database.
#
# The comment below about the login being proxied to Node is also stale: with no
# `-node` flag this server is standalone and authenticates against the live
# `users.json` itself. One cookie still works at both, which is what the diff
# needs, but it works for a different reason than it used to.
#
# `--volumes-from mikrodash` shares VOLUMES, and `/app` is image content — so the
# geoip-lite data does not come with it and the Go server reports the city index
# unavailable. That read as "an environment note, not the port" for weeks, and
# the effect was that `/api/cities` and the whole hand-written .dat parser went
# unverified. `docker cp` puts the data where the parser looks; 18 queries then
# came back byte-identical.
set -eu

NODE=${NODE:-http://127.0.0.1:3081}
GO=${GO:-http://127.0.0.1:3097}
: "${MDU:?set MDU to a dashboard username}"
: "${MDP:?set MDP to that user's password}"

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"; docker rm -f mdverify >/dev/null 2>&1 || true' EXIT

echo "== bringing up the Go server against the live /data =="
docker rm -f mdverify >/dev/null 2>&1 || true
mkdir -p "$TMP/geodata"
for f in geoip-city-names.dat geoip-city.dat; do
  docker cp "mikrodash:/app/node_modules/geoip-lite/data/$f" "$TMP/geodata/" >/dev/null 2>&1 ||
    echo "  (no $f — the city index will report unavailable and its diff proves nothing)"
done
docker run -d --name mdverify --network host --volumes-from mikrodash \
  -v "$ROOT":/src -v "$TMP/geodata":/app/node_modules/geoip-lite/data:ro -w /src \
  golang:1.25-alpine /src/bin/mikrodash -listen :3097 -data /data -web /src/web/dist \
    -no-pool >/dev/null
sleep 3

JAR="$TMP/jar"
# The login goes through the GO server, which proxies it to Node in coexistence
# mode — so one cookie is valid at both, which is what makes the diff possible.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -c "$JAR" \
  -X POST "$GO/api/auth/login" -H 'Content-Type: application/json' \
  --data "{\"username\":\"$MDU\",\"password\":\"$MDP\"}")
[ "$code" = "200" ] || { echo "login failed ($code)"; exit 1; }

RID=$(curl -s -b "$JAR" "$GO/api/routers" |
  python3 -c 'import json,sys;print(json.load(sys.stdin)["routers"][0]["id"])')
TO=$(( $(date +%s) * 1000 ))
FROM=$(( TO - 7 * 86400 * 1000 ))

# KEY-SORTED before comparing. Go's map encoder sorts and JavaScript keeps
# insertion order, and that difference does not reach a browser parsing JSON —
# comparing raw bytes would report every endpoint as differing and hide the
# differences that matter. The ON-DISK half of that question is a real one and is
# handled separately, in internal/store/jsonwrite.go.
#
# AND A FAILURE TO PARSE IS REPORTED, NOT SWALLOWED. This returned "" on bad
# input at first, so two endpoints that BOTH answered 404 HTML normalised to two
# empty strings and compared EQUAL — the first run of this method reported four
# `/api/principals/*` paths as IDENTICAL when neither server served them. A
# comparison that cannot tell agreement from mutual failure is worse than none.
norm() {
  python3 -c 'import json,sys
try:
    print(json.dumps(json.load(sys.stdin), sort_keys=True))
except Exception as e:
    print("NOT-JSON: " + str(e)[:60])' 2>/dev/null || echo "NOT-JSON: reader failed"
}

same=0
differ=0
expected=0

# ── ENDPOINTS THAT CANNOT AGREE FROM A SPARE PROCESS ────────────────────────
#
# `/api/localcc` answers from the CURRENT router session's last WAN address.
# This Go server holds no sessions — it is a second process pointed at the same
# /data, not the one the browser is talking to — so it answers empty while Node
# answers the real address. That is the fixture, not the port, and after cutover
# the Go process is the one holding sessions.
#
# Listed rather than skipped, so the run still shows it was asked and a reader
# can see WHY it differed. A silent omission is how `/api/cities` went
# unverified.
expected_differ() {
  case "$1" in
  /api/localcc*) return 0 ;;
  # `/api/roles` agrees on everything except the ORDER of `writeCapablePages`.
  # The live value is `Object.keys(Rbac.WRITE_CONFERS)` — object insertion order
  # — and the port sorts, because a Go map has no order to preserve and an
  # unsorted answer would differ between runs of the SAME binary.
  #
  # Not user-visible: the page uses it once, as
  # `_rolesMeta.writeCapable.indexOf(page.key) !== -1` — a membership test. The
  # rows are rendered from the page catalogue, not from this list.
  #
  # The MEMBERSHIP is a different question and is pinned properly, by
  # `internal/rbac/tables_test.go` against a corpus generated from the live
  # `WRITE_CONFERS`. If a page ever leaves or joins that set, that test fails —
  # this classification hides only the ordering.
  /api/roles*) return 0 ;;
  *) return 1 ;;
  esac
}

check() {
  g=$(curl -s --max-time 30 -b "$JAR" "$GO$1" | norm)
  n=$(curl -s --max-time 30 -b "$JAR" "$NODE$1" | norm)
  # BOTH SIDES FAILING TO PARSE IS NOT AGREEMENT. Two 404s are equal strings and
  # mean only that the URL is wrong.
  case "$g$n" in
  *NOT-JSON*)
    differ=$((differ + 1))
    printf '  %-52s NOT JSON  go=%s node=%s\n' "$(echo "$1" | cut -c1-52)" \
      "$(echo "$g" | cut -c1-24)" "$(echo "$n" | cut -c1-24)"
    return 0
    ;;
  esac
  if [ "$g" = "$n" ]; then
    same=$((same + 1))
    printf '  %-52s IDENTICAL (%d bytes)\n' "$(echo "$1" | cut -c1-52)" "${#g}"
  elif expected_differ "$1"; then
    expected=$((expected + 1))
    printf '  %-52s differs, EXPECTED (see expected_differ)\n' "$(echo "$1" | cut -c1-52)"
  else
    differ=$((differ + 1))
    printf '  %-52s DIFFERS  go=%d node=%d\n' "$(echo "$1" | cut -c1-52)" "${#g}" "${#n}"
    printf '%s' "$g" > "$TMP/go.json"
    printf '%s' "$n" > "$TMP/node.json"
    python3 -m json.tool "$TMP/go.json" > "$TMP/g.txt" 2>/dev/null || true
    python3 -m json.tool "$TMP/node.json" > "$TMP/n.txt" 2>/dev/null || true
    diff -u "$TMP/g.txt" "$TMP/n.txt" | head -20 | sed 's/^/      /'
  fi
}

echo
echo "== endpoints =="
for u in /api/auth/status /api/auth/permissions /api/routers /api/sites /api/settings \
         /api/nav-prefs /api/localcc /api/account/access /api/account/sessions \
         /api/collectors /api/audit /api/dashboard-layout /api/topology-layout \
         /api/users /api/groups /api/roles /api/grants; do
  check "$u"
done
check "/api/reports/schedules?routerId=$RID"

echo
echo "== the city index, which is a hand-written parser of a 10MB binary format =="
# The folded and case forms are the discriminating ones: a query returning
# NOTHING on both sides agrees about nothing in particular.
for q in berlin munchen krakow tokyo ber BERLIN; do
  check "/api/cities?q=$q"
done

echo
echo "== reports, which read the history database =="
for kind in ping connectivity alerts; do
  check "/api/reports/$kind?routerId=$RID&from=$FROM&to=$TO"
done
# traffic and bandwidth answer with the INTERFACE LIST until one is named, so a
# diff without it compares two short lists and proves nothing about the series.
IF=$(curl -s -b "$JAR" "$GO/api/reports/traffic?routerId=$RID&from=$FROM&to=$TO" |
  python3 -c 'import json,sys;i=json.load(sys.stdin).get("interfaces") or [];print(i[0] if i else "")')
if [ -n "$IF" ]; then
  for kind in traffic bandwidth; do
    check "/api/reports/$kind?routerId=$RID&from=$FROM&to=$TO&interface=$IF"
  done
else
  echo "  (no interfaces in the history; the traffic and bandwidth series are unverified)"
fi

echo
echo "live-diff: $same identical, $differ differing, $expected differing as expected"
[ "$differ" -eq 0 ]
