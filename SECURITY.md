# Security Policy

## Supported Versions

Only the latest release is actively maintained and receives security fixes.

| Version | Supported |
|---------|-----------|
| Latest  | ✅        |
| Older   | ❌        |

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report security issues by emailing the maintainer directly or by using [GitHub's private vulnerability reporting](https://github.com/SecOps-7/MikroDash/security/advisories/new).

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- Any suggested mitigations if you have them

You can expect an acknowledgement within 48 hours and a resolution timeline within 7 days for critical issues.

## Security Considerations

MikroDash stores RouterOS API credentials encrypted at rest (AES-256-GCM). It is designed to run on a trusted internal network. Key points:

- **Do not expose port 3081 to the internet** without a reverse proxy and TLS termination
- Enable the built-in dashboard password in Settings → Security
- Create a dedicated read-only RouterOS API user for MikroDash rather than using the `admin` account
- The `/healthz` endpoint is unauthenticated by design, and so is `POST /api/ztp/enrol`, where a local router pre-provisioned for zero-touch provisioning calls home: it acts only on a valid, unexpired, hashed-at-rest token issued to that device, binds to the router's source address, is rate limited and body capped, and audits every refusal. All other routes require credentials if a dashboard password is set
- **Zero-touch provisioning opens UDP 13231** (WireGuard, userspace) while it is switched on, and it is off by default. Every bootstrap script pins this instance's public key; a remote device's tunnel carries only its own /32, and a device that calls home unannounced reaches only the enrolment endpoint and is never dialled until an administrator approves it. Bootstrap scripts contain secrets (a tunnel key or a token), are shown once, and expire

## Credential profiles hold a password that is valid on many routers

A credential profile (issue #143) provisions a RouterOS account across the
fleet, and every linked device gets **the same password**. That is the operator's
choice and it makes the feature usable, but it changes what `/data` is worth: a
single sealed value in `cred_profiles.secret` now opens an account on every
router the profile is linked to, at whatever privilege it was given.

Compromising the MikroDash host was already serious - it holds the credentials
MikroDash signs in with. This raises the ceiling: those are one account per
router in a group MikroDash chose, while a profile can be `full` on every device
that accepts it.

What follows from that:

- **Treat a profile's password as you would a shared administrator password.**
  Rotate it by editing the profile, which re-writes it on every linked router.
- **Prefer the narrowest permission that works.** `read` is a profile that
  cannot change anything; a custom permission set names exactly what it grants.
- **Unlink before you remove a router from MikroDash.** Removing the router
  leaves the account on the device and drops MikroDash's record of it; the
  removal is audited, naming every account left behind, but nothing afterwards
  can take it off for you.
- **`Forget` abandons accounts on purpose.** It exists for a router that has
  gone for good, and it names every device it walked away from in the audit
  trail. Read that row before you use it.

**What a profile can never do:** change or remove the account MikroDash signs in
with, or move any user into MikroDash's own group. That is refused per router by
`internal/guard/selfguard.go`, which fails closed when it cannot work out which
account is its own. The consequence worth knowing is that a `full` profile is
refused on any router where MikroDash itself is in the `full` group - which is
every device onboarded by zero-touch provisioning.
