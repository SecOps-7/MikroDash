'use strict';

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { JSDOM } = require('jsdom');

const root = path.resolve(__dirname, '..');
const normalise = (value) => String(value || '').replace(/\s+/g, ' ').trim();

function loadChineseLocale() {
  const window = {};
  const source = fs.readFileSync(path.join(root, 'public/locales/zh-CN.js'), 'utf8');
  vm.runInNewContext(source, { window });
  return window.MikroDashLocales['zh-CN'];
}

function staticCandidates(file) {
  const html = fs.readFileSync(path.join(root, 'public', file), 'utf8');
  const dom = new JSDOM(html);
  const candidates = new Set();
  const document = dom.window.document;
  for (const element of document.querySelectorAll('body *')) {
    if (element.matches('script,style,code,pre,textarea,[data-i18n-skip],[data-i18n-user-data]')) continue;
    for (const child of element.childNodes) {
      if (child.nodeType === dom.window.Node.TEXT_NODE) {
        const value = normalise(child.nodeValue);
        if (/[A-Za-z]/.test(value)) candidates.add(value);
      }
    }
    for (const attr of ['placeholder', 'title', 'aria-label']) {
      const value = normalise(element.getAttribute(attr));
      if (/[A-Za-z]/.test(value)) candidates.add(value);
    }
  }
  dom.window.close();
  return candidates;
}

function audit() {
  const locale = loadChineseLocale();
  const policy = JSON.parse(fs.readFileSync(path.join(root, 'i18n/allowlist.json'), 'utf8'));
  const allowed = new Set(Object.values(policy.exact).flat());
  const allowedPatterns = policy.patterns.map((item) => ({ reason: item.reason, regex: new RegExp(item.regex) }));
  const candidates = new Set([...staticCandidates('index.html'), ...staticCandidates('login.html')]);
  const missing = [...candidates].filter((value) =>
    !Object.prototype.hasOwnProperty.call(locale.messages, value) &&
    !allowed.has(value) &&
    !allowedPatterns.some((item) => item.regex.test(value))
  ).sort();
  const staleAllowed = [...allowed].filter((value) => !candidates.has(value)).sort();
  return { candidates: [...candidates].sort(), missing, staleAllowed };
}

if (require.main === module) {
  const result = audit();
  if (result.missing.length) {
    console.error('Unclassified English UI candidates:\n' + result.missing.map((item) => '  - ' + item).join('\n'));
    process.exitCode = 1;
  } else {
    console.log(`i18n audit passed: ${result.candidates.length} static candidates classified.`);
  }
}

module.exports = { audit, staticCandidates };
