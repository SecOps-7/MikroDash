'use strict';

const LEVELS = { debug: 0, info: 1, warn: 2, error: 3, silent: 4 };
const minLevel = LEVELS[String(process.env.LOG_LEVEL || 'info').toLowerCase()] ?? 1;

exports.debug = (...a) => { if (minLevel <= 0) console.debug(...a); };
exports.info  = (...a) => { if (minLevel <= 1) console.log(...a); };
exports.warn  = (...a) => { if (minLevel <= 2) console.warn(...a); };
exports.error = (...a) => { if (minLevel <= 3) console.error(...a); };
