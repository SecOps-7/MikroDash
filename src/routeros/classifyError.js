'use strict';
/**
 * Turns a RouterOS connection error into a human-readable reason (shown in the
 * UI) and an operator hint (logged only).
 *
 * This lived inline in wireRosEvents() and was therefore only reachable for the
 * router a client was actively viewing. The status-only sessions in
 * overviewSessions.js need the same wording so the Routers page can explain
 * *why* a router is offline instead of only saying "Offline". See issue #92.
 *
 * `classified` is false when nothing matched, meaning `reason` is still the raw
 * driver message. Callers must not send an unclassified reason to the browser
 * without passing it through sanitizeErr() first, or substituting their own
 * generic string. The classified strings are fixed text plus the router's own
 * host/user, both of which the caller already supplied.
 */
// node-routeros wraps socket-level failures in a RosException that keeps only
// the numeric errno, so the most common causes arrive with no matching text and
// would otherwise be reported as an opaque "RouterOS API error [-111]".
const ERRNO_ALIAS = {
  '-111': 'ECONNREFUSED',
  '-110': 'ETIMEDOUT',
  '-113': 'EHOSTUNREACH',
  '-101': 'ENETUNREACH',
};

function classifyRosError(err, { host = '', port = '', user = '', tls = false } = {}) {
  const msg = err && err.message ? err.message : String(err);
  // Match against the message plus any errno alias, so a numeric errno reaches
  // the same branch as the textual code. `msg` itself is left untouched, since
  // `classified` is derived from it.
  const alias = (err && ERRNO_ALIAS[String(err.errno)]) || '';
  const probe = alias ? `${msg} ${alias}` : msg;
  let reason = msg;
  let hint   = '';

  if (/EHOSTUNREACH|ENETUNREACH/.test(probe)) {
    reason = `Network unreachable — no route to ${host} from the MikroDash host`;
    hint   = `Check routing/VLAN between this container and ${host}`;
  } else if (/ECONNREFUSED/.test(probe)) {
    reason = `Connection refused — is RouterOS reachable at ${host}?`;
    hint   = `Check that the RouterOS API service is enabled: /ip service set api${tls ? '-ssl' : ''} disabled=no`;
  } else if (/ETIMEDOUT/.test(probe) || /timed out/i.test(probe)) {
    reason = 'Connection timed out — check host and firewall rules';
    hint   = `Verify ${host}:${port} is reachable and not blocked by a firewall rule`;
  } else if (/ENOTFOUND/.test(probe) || /ENOENT/.test(probe)) {
    reason = `Host not found — check router host setting (${host})`;
    hint   = 'Ensure the hostname or IP address is correct and DNS is resolving';
  } else if (/ECONNRESET/.test(probe)) {
    reason = 'Connection reset by router';
    hint   = 'The router closed the connection unexpectedly — check RouterOS logs';
  } else if (/certificate/i.test(msg)) {
    reason = 'TLS certificate error — try enabling "Allow self-signed cert"';
    hint   = 'Set tlsInsecure=true in settings or use a valid certificate on the router';
  } else if (/authentication/i.test(msg) || /login/i.test(msg) || /invalid user/i.test(msg) || /wrong password/i.test(msg) || /username.*invalid|password.*invalid/i.test(msg) || (err && err.errno === 'CANTLOGIN')) {
    reason = 'Authentication failed — check username and password';
    hint   = `Confirm user "${user}" exists on the router and has API access: /user print`;
  } else if (/RosException/.test(msg) || (err && err.name === 'RosException')) {
    const errno = err && err.errno ? err.errno : '';
    if (tls) {
      reason = `TLS handshake failed — check that RouterOS api-ssl is enabled${errno ? ` [${errno}]` : ''}`;
      hint   = 'Run: /ip service set api-ssl disabled=no  — and verify the certificate is valid';
    } else {
      reason = `RouterOS API error${errno ? ` [${errno}]` : ''} — check that the API service is enabled and the user has API access`;
      hint   = `Run: /ip service set api disabled=no  — then confirm user "${user}" has API group permissions`;
    }
  }

  return { reason, hint, msg, classified: reason !== msg };
}

module.exports = { classifyRosError };
