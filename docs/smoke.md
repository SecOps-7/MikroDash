# Smoke Test Checklist

Run this after every merge that should go into a production container.

## Pre-checks

- `docker compose ps` shows the container as `running`
- `curl -fsS http://127.0.0.1:3081/healthz` returns `200`
- `docker compose logs --tail=100 mikrodash` shows a healthy RouterOS connection

The startup banner states what is switched on — history recording, the backup
scheduler, alert dispatch, the retention sweep — and whether the process is
standalone. Read it: each of those lines is the app telling you what it will do,
and a surprise there is worth resolving before the UI checks.

## UI checks

- Open the dashboard and confirm sign-in works.
- Dashboard page renders without blank cards.
- Interfaces page shows active interfaces and non-negative throughput.
- DHCP page loads leases.
- VPN page loads WireGuard peers or renders a clean empty state.
- Logs page updates live.

## RouterOS data checks

- The WAN interface matches the router's configured default interface.
- `ifStatus` shows non-zero throughput on an active interface.
- Connection counts update over time.
- If the router has wireless or WireGuard disabled, the UI still renders without errors.

## Resilience checks

- Restart the container and confirm the dashboard recovers:

```bash
docker compose restart mikrodash
curl -fsS http://127.0.0.1:3081/healthz
```

- If safe in your lab, briefly interrupt API access and confirm the app reconnects once access returns.

## Release gate

Treat the build as releasable only when:

- the smoke checklist passes
- `sh tools/verify.sh` is green on the same commit
- no unexpected reconnect loops appear in container logs
