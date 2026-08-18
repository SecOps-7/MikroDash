'use strict';

// These are the only translation files needed by the signed-out page. Keep the
// list exact: a prefix such as /locales/ would accidentally make future files
// public without a security review.
const PUBLIC_I18N_PATHS = new Set([
  '/i18n.js',
  '/locales/en-US.js',
  '/locales/zh-CN.js',
]);

function isPublicI18nPath(requestPath) {
  return PUBLIC_I18N_PATHS.has(requestPath);
}

module.exports = { PUBLIC_I18N_PATHS, isPublicI18nPath };
