'use strict';

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { JSDOM } = require('jsdom');
const espree = require('espree');

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
    for (const attr of ['placeholder', 'title', 'alt', 'aria-label', 'aria-description', 'aria-valuetext']) {
      const value = normalise(element.getAttribute(attr));
      if (/[A-Za-z]/.test(value)) candidates.add(value);
    }
  }
  dom.window.close();
  return candidates;
}

function _walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  visit(node);
  for (const [key, value] of Object.entries(node)) {
    if (key === 'parent') continue;
    if (Array.isArray(value)) value.forEach((item) => _walk(item, visit));
    else if (value && typeof value.type === 'string') _walk(value, visit);
  }
}

function dynamicCandidates(file) {
  const source = fs.readFileSync(path.join(root, 'public', file), 'utf8');
  const ast = espree.parse(source, { ecmaVersion: 2022, sourceType: 'script' });
  const definitions = new Map();
  _walk(ast, (node) => {
    if (node.type === 'VariableDeclarator' && node.id.type === 'Identifier' && node.init) {
      if (!definitions.has(node.id.name)) definitions.set(node.id.name, []);
      definitions.get(node.id.name).push(node.init);
    }
    if (node.type === 'AssignmentExpression' && node.operator === '=' &&
        node.left.type === 'Identifier' && node.right) {
      if (!definitions.has(node.left.name)) definitions.set(node.left.name, []);
      definitions.get(node.left.name).push(node.right);
    }
  });

  const candidates = new Set();
  const addLiteral = (value) => {
    if (typeof value !== 'string' || !/[A-Za-z]{2}/.test(value)) return;
    if (/^(?:dis$|dashboard$|routes$|padding:|[.#"]|rgba\(|(?:bw-proto|diag-count|hs-|ifl-|wl-band))|(?:class|style|viewBox|width)="|cursor:|px">/.test(value)) return;
    if (!value.includes('<')) {
      const text = normalise(value);
      if (/[A-Za-z]{2}/.test(text)) candidates.add(text);
      return;
    }
    const fragment = JSDOM.fragment(value);
    const visit = (node) => {
      if (node.nodeType === 3) {
        const text = normalise(node.nodeValue);
        if (/[A-Za-z]{2}/.test(text)) candidates.add(text);
      }
      if (node.nodeType === 1) {
        for (const attr of ['placeholder', 'title', 'alt', 'aria-label', 'aria-description', 'aria-valuetext']) {
          const text = normalise(node.getAttribute(attr));
          if (/[A-Za-z]{2}/.test(text)) candidates.add(text);
        }
      }
      for (const child of node.childNodes || []) visit(child);
    };
    visit(fragment);
  };
  const combine = (left, right) => {
    if (!left || !right) return null;
    return left.flatMap((a) => right.map((b) => a + b)).slice(0, 64);
  };
  const renderExpression = (node, resolving = new Set()) => {
    if (!node) return null;
    if (node.type === 'Literal') return typeof node.value === 'string' ? [node.value] : null;
    if (node.type === 'TemplateLiteral') {
      if (node.expressions.length) return null;
      let values = [''];
      node.quasis.forEach((quasi) => {
        values = combine(values, [quasi.value.cooked || '']);
      });
      return values;
    }
    if (node.type === 'BinaryExpression' && node.operator === '+') {
      return combine(renderExpression(node.left, resolving), renderExpression(node.right, resolving));
    }
    if (node.type === 'ConditionalExpression') {
      const consequent = renderExpression(node.consequent, resolving);
      const alternate = renderExpression(node.alternate, resolving);
      return consequent && alternate ? [...consequent, ...alternate].slice(0, 64) : null;
    }
    if (node.type === 'LogicalExpression') {
      const left = renderExpression(node.left, resolving);
      const right = renderExpression(node.right, resolving);
      return left && right ? [...left, ...right].slice(0, 64) : null;
    }
    if (node.type === 'Identifier' && definitions.has(node.name) && !resolving.has(node.name)) {
      const next = new Set(resolving).add(node.name);
      const simpleChoices = definitions.get(node.name).filter((definition) =>
        definition.type === 'Literal' || definition.type === 'ConditionalExpression' || definition.type === 'LogicalExpression');
      const rendered = simpleChoices.map((definition) => renderExpression(definition, next)).filter(Boolean);
      return rendered.length ? rendered.flat().slice(0, 64) : null;
    }
    return null;
  };
  const collectExpression = (expression) => {
    const whole = renderExpression(expression);
    if (whole) whole.forEach(addLiteral);
    // A dynamic HTML expression can still contain complete static text nodes,
    // and a simple local status label may be concatenated into the sink.
    _walk(expression, (node) => {
      if (node.type === 'Literal' && typeof node.value === 'string' && node.value.includes('<')) addLiteral(node.value);
      if (node.type === 'Identifier' && definitions.has(node.name)) {
        const rendered = renderExpression(node);
        if (rendered) rendered.forEach(addLiteral);
      }
    });
  };

  const sinkProperties = new Set(['textContent', 'innerHTML']);
  const translatedAttrs = new Set(['placeholder', 'title', 'alt', 'aria-label', 'aria-description', 'aria-valuetext']);
  _walk(ast, (node) => {
    if (node.type === 'AssignmentExpression' && node.left.type === 'MemberExpression' && !node.left.computed &&
        sinkProperties.has(node.left.property.name)) {
      collectExpression(node.right);
    }
    if (node.type !== 'CallExpression' || node.callee.type !== 'MemberExpression' || node.callee.computed) return;
    if (node.callee.property.name === 'insertAdjacentHTML' && node.arguments[1]) collectExpression(node.arguments[1]);
    if (node.callee.property.name === 'setAttribute' && node.arguments[0] &&
        node.arguments[0].type === 'Literal' && translatedAttrs.has(node.arguments[0].value) && node.arguments[1]) {
      collectExpression(node.arguments[1]);
    }
  });
  // Explicit translation calls are UI copy even when their result is joined
  // with user data before it reaches a DOM sink. Without this pass, those
  // fragments look like unused locale entries and a missing translation can
  // hide inside a sentence that is only partly Chinese.
  _walk(ast, (node) => {
    if (node.type !== 'CallExpression' || !node.arguments[0]) return;
    const direct = node.callee.type === 'Identifier' && node.callee.name === 'tr';
    const apiCall = node.callee.type === 'MemberExpression' && !node.callee.computed &&
      node.callee.property.name === 't';
    if (direct || apiCall) collectExpression(node.arguments[0]);
  });
  return candidates;
}

function audit() {
  const locale = loadChineseLocale();
  const policy = JSON.parse(fs.readFileSync(path.join(root, 'i18n/allowlist.json'), 'utf8'));
  const allowed = new Set(Object.values(policy.exact).flat());
  const allowedPatterns = policy.patterns.map((item) => ({ reason: item.reason, regex: new RegExp(item.regex) }));
  const staticSet = new Set([...staticCandidates('index.html'), ...staticCandidates('login.html')]);
  const dynamicFiles = ['app.js', 'login.js', 'preflight.js',
    path.join('js', 'dashboard-grid.js'), path.join('js', 'topology.js')];
  const dynamicSet = new Set(dynamicFiles.flatMap((file) => [...dynamicCandidates(file)]));
  const candidates = new Set([...staticSet, ...dynamicSet]);
  const missing = [...candidates].filter((value) =>
    !Object.prototype.hasOwnProperty.call(locale.messages, value) &&
    !allowed.has(value) &&
    !allowedPatterns.some((item) => item.regex.test(value))
  ).sort();
  const staleAllowed = [...allowed].filter((value) => !candidates.has(value)).sort();
  // Visibility only: some keys are assembled from bounded runtime values (for
  // example document titles), so an unmatched entry is a cleanup lead rather
  // than a release failure. Missing current UI copy remains the hard failure.
  const staleMessages = Object.keys(locale.messages).filter((value) => !candidates.has(value)).sort();
  return {
    candidates: [...candidates].sort(),
    staticCandidates: [...staticSet].sort(),
    dynamicCandidates: [...dynamicSet].sort(),
    missing, staleAllowed, staleMessages,
  };
}

if (require.main === module) {
  const result = audit();
  if (result.missing.length) {
    console.error('Unclassified English UI candidates:\n' + result.missing.map((item) => '  - ' + item).join('\n'));
    process.exitCode = 1;
  } else {
    console.log(`i18n audit passed: ${result.staticCandidates.length} static HTML and ${result.dynamicCandidates.length} dynamic UI candidates classified; ${result.staleMessages.length} locale entries have no static match (visibility only).`);
  }
}

module.exports = { audit, staticCandidates, dynamicCandidates };
