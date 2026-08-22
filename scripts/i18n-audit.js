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
  const ast = espree.parse(source, { ecmaVersion: 2022, sourceType: 'script', loc: true, range: true });
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

  const functionScopes = [];
  _walk(ast, (node) => {
    if (['FunctionDeclaration', 'FunctionExpression', 'ArrowFunctionExpression'].includes(node.type)) {
      functionScopes.push(node);
    }
  });
  const scopeKey = (node) => {
    const matches = functionScopes.filter((fn) => fn.range[0] <= node.range[0] && fn.range[1] >= node.range[1])
      .sort((a, b) => (a.range[1] - a.range[0]) - (b.range[1] - b.range[0]));
    return matches.length ? `${matches[0].range[0]}:${matches[0].range[1]}` : 'global';
  };
  const scopedDefinitions = new Map();
  _walk(ast, (node) => {
    let name = '', value = null;
    if (node.type === 'VariableDeclarator' && node.id.type === 'Identifier' && node.init) {
      name = node.id.name; value = node.init;
    } else if (node.type === 'AssignmentExpression' && node.operator === '=' &&
               node.left.type === 'Identifier' && node.right) {
      name = node.left.name; value = node.right;
    }
    if (!name) return;
    if (!scopedDefinitions.has(name)) scopedDefinitions.set(name, []);
    scopedDefinitions.get(name).push({ value, scope: scopeKey(value) });
  });

  const candidates = new Set();
  const unresolvedTranslations = [];
  const untranslatedDialogs = [];
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
    if (direct) {
      const rendered = renderExpression(node.arguments[0]);
      if (rendered) rendered.forEach(addLiteral);
      else unresolvedTranslations.push(
        `${file}:${node.loc.start.line}: ${source.slice(node.range[0], node.range[1])}`
      );
    } else if (apiCall) collectExpression(node.arguments[0]);
  });

  const isDirectTranslation = (node) => node && node.type === 'CallExpression' &&
    node.callee.type === 'Identifier' && node.callee.name === 'tr';
  const nearestDefinition = (name, anchor) => {
    const anchorScope = scopeKey(anchor);
    const items = (scopedDefinitions.get(name) || []).filter((item) =>
      item.scope === anchorScope && item.value.loc && item.value.loc.start.line <= anchor.loc.start.line);
    const hit = items.sort((a, b) => b.value.loc.start.line - a.value.loc.start.line)[0];
    return hit ? hit.value : null;
  };
  const addDialogIssue = (node, value) => {
    const text = normalise(value);
    if (!/[A-Za-z]{2}/.test(text)) return;
    untranslatedDialogs.push(`${file}:${node.loc.start.line}: ${text}`);
  };
  const scanDialogExpression = (node, anchor, resolving = new Set()) => {
    if (!node) return;
    if (isDirectTranslation(node)) return;
    if (node.type === 'Literal') {
      if (typeof node.value === 'string') addDialogIssue(node, node.value);
      return;
    }
    if (node.type === 'TemplateLiteral') {
      node.quasis.forEach((quasi) => addDialogIssue(quasi, quasi.value.cooked || ''));
      node.expressions.forEach((item) => scanDialogExpression(item, anchor, resolving));
      return;
    }
    if (node.type === 'Identifier') {
      if (resolving.has(node.name)) return;
      const definition = nearestDefinition(node.name, anchor);
      if (definition) scanDialogExpression(definition, anchor, new Set(resolving).add(node.name));
      return;
    }
    if (node.type === 'ObjectExpression') {
      node.properties.forEach((property) => scanDialogExpression(property.value, anchor, resolving));
      return;
    }
    if (node.type === 'ConditionalExpression') {
      scanDialogExpression(node.consequent, anchor, resolving);
      scanDialogExpression(node.alternate, anchor, resolving);
      return;
    }
    if (node.type === 'BinaryExpression') {
      if (node.operator === '+') {
        scanDialogExpression(node.left, anchor, resolving);
        scanDialogExpression(node.right, anchor, resolving);
      }
      return;
    }
    if (node.type === 'LogicalExpression') {
      scanDialogExpression(node.left, anchor, resolving);
      scanDialogExpression(node.right, anchor, resolving);
      return;
    }
    if (node.type === 'MemberExpression') {
      scanDialogExpression(node.object, anchor, resolving);
      return;
    }
    if (node.type === 'CallExpression') {
      if (node.callee.type === 'MemberExpression' && !node.callee.computed &&
          node.callee.property.name === 'join') {
        scanDialogExpression(node.callee.object, anchor, resolving);
      }
      return;
    }
    for (const [key, value] of Object.entries(node)) {
      if (['loc', 'range', 'start', 'end', 'raw'].includes(key)) continue;
      if (Array.isArray(value)) value.forEach((item) => {
        if (item && typeof item.type === 'string') scanDialogExpression(item, anchor, resolving);
      });
      else if (value && typeof value.type === 'string') scanDialogExpression(value, anchor, resolving);
    }
  };
  _walk(ast, (node) => {
    if (node.type !== 'CallExpression' || !node.arguments[0]) return;
    const direct = node.callee.type === 'Identifier' && ['confirm', 'prompt', 'alert'].includes(node.callee.name);
    const member = node.callee.type === 'MemberExpression' && !node.callee.computed &&
      ['confirm', 'prompt', 'alert'].includes(node.callee.property.name);
    if (direct || member) scanDialogExpression(node.arguments[0], node);
  });
  candidates.unresolvedTranslations = [...new Set(unresolvedTranslations)];
  candidates.untranslatedDialogs = [...new Set(untranslatedDialogs)];
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
  const dynamicResults = dynamicFiles.map((file) => dynamicCandidates(file));
  const dynamicSet = new Set(dynamicResults.flatMap((result) => [...result]));
  const unresolvedTranslations = dynamicResults.flatMap((result) => result.unresolvedTranslations || []);
  const untranslatedDialogs = dynamicResults.flatMap((result) => result.untranslatedDialogs || []);
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
    missing, staleAllowed, staleMessages, unresolvedTranslations, untranslatedDialogs,
  };
}

if (require.main === module) {
  const result = audit();
  if (result.missing.length || result.unresolvedTranslations.length || result.untranslatedDialogs.length) {
    if (result.missing.length) {
      console.error('Unclassified English UI candidates:\n' + result.missing.map((item) => '  - ' + item).join('\n'));
    }
    if (result.unresolvedTranslations.length) {
      console.error('Unresolved explicit translation calls:\n' + result.unresolvedTranslations.map((item) => '  - ' + item).join('\n'));
    }
    if (result.untranslatedDialogs.length) {
      console.error('Untranslated native-dialog text:\n' + result.untranslatedDialogs.map((item) => '  - ' + item).join('\n'));
    }
    process.exitCode = 1;
  } else {
    console.log(`i18n audit passed: ${result.staticCandidates.length} static HTML and ${result.dynamicCandidates.length} dynamic UI candidates classified; ${result.staleMessages.length} locale entries have no static match (visibility only).`);
  }
}

module.exports = { audit, staticCandidates, dynamicCandidates };
