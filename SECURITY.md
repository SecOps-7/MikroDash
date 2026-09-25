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
