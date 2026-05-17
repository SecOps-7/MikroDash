function buildHelmetOptions() {
  return {
    hsts: process.env.FORCE_HTTPS === 'true' ? { maxAge: 63072000 } : false,
    contentSecurityPolicy: {
      directives: {
        defaultSrc: ["'self'"],
        scriptSrc: ["'self'"],
        styleSrc: ["'self'", "'unsafe-inline'"],
        fontSrc: ["'self'"],
        connectSrc: ["'self'"],
        imgSrc: ["'self'", 'data:'],
        upgradeInsecureRequests: null, // disabled — dashboard may be served over plain HTTP on LAN
      },
    },
  };
}

module.exports = { buildHelmetOptions };
