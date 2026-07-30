const DEFAULT_FRESHNESS_MS = {
  traffic:  20000,
  system:   30000,
  ifstatus: 120000,
};

function computeHealthStatus({ startupReady, rosConnected, state = {}, now = Date.now(), freshnessMs = DEFAULT_FRESHNESS_MS, requiredCollectors = ['traffic'] }) {
  const stale = [];
  if (startupReady && rosConnected) {
    const checks = {
      traffic: state.lastTrafficTs,
      system: state.lastSystemTs,
      ifstatus: state.lastIfStatusTs,
    };
    for (const name of requiredCollectors) {
      const ts = checks[name];
      const maxAge = freshnessMs[name];
      if (!Number.isFinite(maxAge) || !Number.isFinite(ts) || ts <= 0 || now - ts > maxAge) stale.push(name);
    }
  }
  const ok = !!startupReady && !!rosConnected && stale.length === 0;
  return {
    ok,
    statusCode: ok ? 200 : 503,
    stale,
  };
}

module.exports = { computeHealthStatus, DEFAULT_FRESHNESS_MS };
