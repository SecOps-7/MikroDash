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

> MikroDash is now a single static binary in a ~180 MB image, so running it *on*
> a router that supports containers is more realistic than it was. This document
> still describes the external-host layout, which remains the safer default: a
> dashboard that dies with the router it is monitoring cannot tell you the router
> died.

## Deploy on the R5S

**There is no `.env` to prepare and no router credentials to put in a file.**
Router details, dashboard accounts and the encryption key are all managed through
the web UI and stored in the Docker volume. Earlier versions of this document set
`ROUTER_HOST`, `BASIC_AUTH_USER` and similar; none of those are read.

1. Create `/opt/mikrodash/docker-compose.yml`:

```yaml
services:
  mikrodash:
    image: ghcr.io/secops-7/mikrodash:latest
    restart: unless-stopped
    ports:
      - "3081:3081"
    volumes:
      - mikrodash-data:/data

volumes:
  mikrodash-data:
```

2. Start it:

```bash
cd /opt/mikrodash
docker compose up -d
```

3. Verify startup:

```bash
docker compose ps
docker compose logs --tail=100 mikrodash
curl -fsS http://127.0.0.1:3081/healthz
```

4. Open `http://<R5S_IP>:3081` and complete the first-run wizard: create the
   dashboard account, then add the `hEX S` with the API user created below.

## RouterOS setup on the hEX S

Create a dedicated read-only API user and only allow the `R5S` host to reach the API.

```routeros
/ip service set api port=8728 disabled=no
/user group add name=mikrodash policy=read,api,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!test,!winbox,!web,!sniff,!sensitive,!romon,!rest-api
/user add name=mikrodash group=mikrodash password=change-me
```

Read-only is the right default here. If you want the Packages page add `write`; if you also want the
Router Users page add `policy` as well — `policy` is what governs RouterOS user management, so an
account holding it can create router users. Scheduled backups additionally need `ftp`, because that
is the policy RouterOS requires to read a backup file off the device. See the README for the full
trade-off.

Restrict API access to the `R5S` management IP:

```routeros
/ip firewall filter add chain=input action=accept src-address=<R5S_IP> protocol=tcp dst-port=8728 comment="MikroDash API from R5S"
/ip firewall filter add chain=input action=drop protocol=tcp dst-port=8728 comment="Drop MikroDash API from others"
```

## Safety notes

- Risk: firewall changes on the MikroTik can cut off remote management. Apply API allow rules before any drop rules and keep an out-of-band path if possible.
- Do not expose the dashboard directly to the Internet.
- If you put a reverse proxy in front, terminate TLS there and set `FORCE_HTTPS=true` so session cookies are marked Secure.

## Rollout recommendation

- Build and test a staging tag
- Run the smoke checklist in `docs/smoke.md`
- Promote the same commit to a production tag only after the smoke passes
