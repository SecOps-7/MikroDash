# Cutover — 2026-08-30

The Go + TypeScript port serves MikroDash on **:3081**. The Node app is stopped.

| | |
|---|---|
| serving | `mikrodash-go` from the **`mikrodash-go:latest` image** (180 MB), `restart=unless-stopped`, `-p 3081:3081` |
| data | the LIVE volume `mikrodash_data` (schema v15), no longer a copy |
| flags | baked into the image's `CMD`: `-node=` (standalone) `-history -backup-scheduler -retention -alert-dispatch`. Override by giving the container its own arguments. |
| stopped | `mikrodash` — the Node app, container and image both retained |
| pre-cutover backup | `/DATA/Backup/Projects/mikrodash_data-precutover-20260830-105321.tgz` (45 MB) |

The backup was taken **after** Node stopped, so SQLite had checkpointed its WAL
and `mikrodash.db` alone is a consistent snapshot. That is why no `-wal`/`-shm`
appear in the archive, and it is the difference between a restorable backup and
a torn one.

## Rollback — back on Node in one command

```sh
docker stop mikrodash-go && docker start mikrodash
```

Both keep `restart=unless-stopped`, so whichever is running survives a reboot and
the other stays down. Nothing else binds 3081, and `mikrodash` is alone on its
network, so the swap is the whole operation.

**Rolling back does NOT rewind the data.** Go has been writing to the same
volume — history samples, alert rows, audit rows, scheduled backups. Node reads
all of it (that is what `cmd/compat` proves, from the other direction). Restore
the tarball only if the data itself is wrong, and only with both apps stopped:

```sh
docker stop mikrodash-go mikrodash
docker run --rm -v mikrodash_data:/data -v /DATA/Backup/Projects:/b alpine \
  sh -c 'rm -rf /data/* /data/.secret && tar xzf /b/mikrodash_data-precutover-20260830-105321.tgz -C /data'
docker start mikrodash
```

## What cutover closed

Blockers 3, 4 and 5 existed **only because both apps ran at once**, and stopping
Node is what closes them rather than any code change:

- **settings.json** — `src/settings.js` cached the whole object at first load and
  never re-read it, so a Go write was invisible to Node and reverted by its next
  save. No second writer now.
- **routers.json** — the identical `if (_cache) return _cache;` pattern. The
  routes it forbade exercising (`POST/PUT/DELETE /api/routers`, `PUT
  /api/sites/:id/routers`, `DELETE /api/sites/:id`) are now safe.
- **notification transports** — `-alert-dispatch` is ON. It was off because both
  engines evaluated the same conditions with in-memory cooldowns neither could
  see, and a duplicated Telegram message cannot be un-received. One engine now.
- **principal writes** (users, groups, roles, grants) were registered behind
  `if s.standalone` because Node memoises RBAC views on a generation counter only
  its own `bump()` advances. Standalone is now true.

## Two defects the cutover itself found

1. **`-node` defaulted to `http://127.0.0.1:3081`.** Correct while Node owned
   that port; at cutover it made the app proxy every un-ported route TO ITSELF,
   and silently left the background pool and the retention sweep off, because
   both are gated on `standalone` — which means "no `-node`". Three subsystems
   wrong from one stale default, visible only in a startup line. The default is
   now empty and `mustNotProxyToSelf` refuses the loop by host and port rather
   than by string equality.
2. **Twelve generators would have become a permanent SKIP.** They `docker exec`
   into the running Node container for `better-sqlite3`/`pdfkit`. Stopping it
   turned twelve gates into "NOT CHECKED" while the sweep still printed green.
   The dependency was never the running container but the IMAGE's node_modules,
   so `verify.sh` now falls back to a throwaway `docker run`. All 12 check again.

## The image

The first hours after cutover ran the binary out of a **mounted repo** under
`golang:1.25-alpine`. That is how the port was developed and it worked, but it
is not a deployment artifact: it depends on the repo staying at one path on one
host, and ships a Go toolchain to production to run a static binary.

`Dockerfile` is a three-stage build — the geo database, then the binary (which
also builds the frontend through esbuild's Go API, so no Node stage exists),
(`CGO_ENABLED=0`, which is what `modernc.org/sqlite` being pure Go buys), the
and an Alpine runtime. **180 MB against the Node image's 775 MB.**
The only mount is `/data`; the binary and both asset trees live in the image.

```sh
docker build -t mikrodash-go:latest .
docker rm -f mikrodash-go
docker run -d --name mikrodash-go --restart unless-stopped \
  --network mikrodash_default -p 3081:3081 -v mikrodash_data:/data \
  mikrodash-go:latest
```

Two runtime packages are there for reasons that only show up late:
**`docker restart` is NOT a redeploy.** It keeps the image the container was
created from, so a rebuilt image is ignored while the app comes back looking
perfectly healthy. Measured 2026-08-30: after rebuilding with the new geo
backend, a restarted container still had no `/app/geo` and the city picker still
answered from the legacy reader. Always `docker rm -f` then `docker run`, as
above.

`ca-certificates`, because the notification transports talk TLS to Telegram,
SMTP and ntfy and an image with no roots fails all three at the moment they
matter; and `tzdata`, because `alertTimestamp` calls `time.LoadLocation` with
the install's display zone and falls back to UTC without it.

**The geo database is fetched fresh at build** by the `geodata` stage — DB-IP
City Lite, no account and no licence key. It replaced copying geoip-lite's
`.dat` files out of the Node image on 2026-08-30, which removed the last
build-time tie to that image. `-geo` still points anywhere, so a volume can
supply your own database without a rebuild.