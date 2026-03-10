function computeHealthStatus({ startupReady, rosConnected }) {
  const ok = !!startupReady && !!rosConnected;
  return {
    ok,
    statusCode: ok ? 200 : 503,
  };
}

module.exports = { computeHealthStatus };
