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
- The `/healthz` endpoint is unauthenticated by design; all other routes require credentials if a dashboard password is set
