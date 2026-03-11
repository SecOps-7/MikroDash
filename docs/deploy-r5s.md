# Production Deployment: R5S + MikroTik hEX S

## Topology

- `R5S` runs the `MikroDash` container.
- `hEX S` stays a plain RouterOS device and only exposes the API service.
- Operators open the dashboard on the `R5S` host, not on the router itself.

```
Operator browser -> R5S:3081 -> MikroDash -> RouterOS API -> hEX S
```

## Why this layout

- Keeps dashboard CPU and memory load off the router.
- Works even when the MikroTik model does not support containers.
- Gives you standard Docker logging, restart policy, and rollback on the `R5S`.

## Files

- `deploy/r5s/.env.example` — starting point for production settings
- `deploy/r5s/docker-compose.yml` — Compose stack for the `R5S`
- `docs/smoke.md` — post-deploy smoke checklist

## Deploy on the R5S

1. Copy this repository to the `R5S`.
2. Prepare the environment file:

```bash
cd /opt/mikrodash
cp deploy/r5s/.env.example deploy/r5s/.env
chmod 600 deploy/r5s/.env
```

3. Edit `deploy/r5s/.env` and set:
   - `ROUTER_HOST` to the `hEX S` management IP
   - `ROUTER_USER` / `ROUTER_PASS` to a dedicated read-only API user
   - `DEFAULT_IF` to the WAN interface you want on the main chart
   - `BASIC_AUTH_USER` / `BASIC_AUTH_PASS` to non-default credentials

4. Start the stack:

```bash
cd /opt/mikrodash/deploy/r5s
docker compose up -d --build
```

5. Verify startup:

```bash
docker compose ps
docker compose logs --tail=100 mikrodash
curl -fsS http://127.0.0.1:3081/healthz
```

## RouterOS setup on the hEX S

Create a dedicated read-only API user and only allow the `R5S` host to reach the API.

```routeros
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!test,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=change-me
```

Restrict API access to the `R5S` management IP:

```routeros
/ip firewall filter add chain=input action=accept src-address=<R5S_IP> protocol=tcp dst-port=8728 comment="MikroDash API from R5S"
/ip firewall filter add chain=input action=drop protocol=tcp dst-port=8728 comment="Drop MikroDash API from others"
```

## Safety notes

- Risk: firewall changes on the MikroTik can cut off remote management. Apply API allow rules before any drop rules and keep an out-of-band path if possible.
- Do not expose the dashboard directly to the Internet.
- If you later put a reverse proxy in front, keep Basic Auth enabled in MikroDash as a second layer.

## Rollout recommendation

- Build and test as `hordesnake1/mikrodash:staging`
- Run the smoke checklist in `docs/smoke.md`
- Promote the same commit to a production tag only after the smoke passes
