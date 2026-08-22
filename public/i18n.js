/* eslint-env browser */
(function (global) {
  'use strict';
  var STORAGE_KEY = 'mikrodash-language';
  var DEFAULT_LANGUAGE = 'en-US';
  var SUPPORTED = ['en-US', 'zh-CN'];
  var TRANSLATABLE_ATTRIBUTES = [
    'placeholder', 'title', 'alt', 'aria-label', 'aria-description', 'aria-valuetext'
  ];
  var SKIP_SELECTOR = 'script,style,code,pre,textarea,[data-i18n-skip],[data-i18n-user-data]';
  var textState = new WeakMap();
  var attrState = new WeakMap();
  var applying = false;
  var observer = null;
  var auditEnabled = false;
  var misses = Object.create(null);

  function normaliseLanguage(value) {
    var raw = String(value || '').trim().replace(/_/g, '-').toLowerCase();
    if (raw === 'zh' || raw === 'zh-cn' || raw === 'zh-sg' || raw === 'zh-hans' || raw.indexOf('zh-hans-') === 0) return 'zh-CN';
    return DEFAULT_LANGUAGE;
  }

  function initialLanguage() {
    try {
      var saved = localStorage.getItem(STORAGE_KEY);
      if (saved && SUPPORTED.indexOf(saved) !== -1) return saved;
    } catch (_) {}
    return normaliseLanguage(navigator.language || navigator.userLanguage);
  }

  var currentLanguage = initialLanguage();
  function locale() {
    var locales = global.MikroDashLocales || {};
    return locales[currentLanguage] || locales[DEFAULT_LANGUAGE] || { messages: {}, patterns: [] };
  }

  function looksLikeUiCopy(source) {
    return /[A-Za-z]/.test(source) && source.length <= 240 && !/^(?:https?:|wss?:|[\w.-]+@[\w.-]+|[\da-f:/.]+)$/i.test(source);
  }

  function recordMiss(source, context) {
    if (!auditEnabled || currentLanguage === DEFAULT_LANGUAGE || !looksLikeUiCopy(source)) return;
    var key = source.replace(/\s+/g, ' ').trim();
    if (!key) return;
    if (!misses[key]) misses[key] = { text: key, count: 0, contexts: [] };
    misses[key].count += 1;
    if (context && misses[key].contexts.indexOf(context) === -1) misses[key].contexts.push(context);
  }

  function translateSource(source, context) {
    if (currentLanguage === DEFAULT_LANGUAGE || !source) return source;
    var active = locale();
    var messages = active.messages || {};
    if (Object.prototype.hasOwnProperty.call(messages, source)) return messages[source];
    var normalisedSource = source.replace(/\s+/g, ' ').trim();
    if (normalisedSource !== source && Object.prototype.hasOwnProperty.call(messages, normalisedSource)) return messages[normalisedSource];
    var patterns = active.patterns || [];
    for (var i = 0; i < patterns.length; i++) {
      var item = patterns[i];
      var match = source.match(item[0]);
      if (match) {
        return source.replace(item[0], function () {
          var args = Array.prototype.slice.call(arguments, 0, -2);
          return typeof item[1] === 'function' ? item[1].apply(null, args) : item[1];
        });
      }
    }
    recordMiss(source, context);
    return source;
  }

  function translateText(value, context) {
    var match = String(value || '').match(/^(\s*)([\s\S]*?)(\s*)$/);
    if (!match || !match[2]) return value;
    return match[1] + translateSource(match[2], context) + match[3];
  }

  function shouldSkip(node) {
    var element = node && (node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement);
    return !!(element && element.closest(SKIP_SELECTOR));
  }

  function contextFor(element, suffix) {
    if (!element) return suffix || 'text';
    var id = element.id ? '#' + element.id : '';
    return element.tagName.toLowerCase() + id + (suffix ? '[' + suffix + ']' : '');
  }

  function translateTextNode(node) {
    if (!node || node.nodeType !== Node.TEXT_NODE || shouldSkip(node)) return;
    var state = textState.get(node);
    var value = node.nodeValue;
    if (!state || value !== state.rendered) {
      state = { source: value, rendered: value };
      textState.set(node, state);
    }
    var next = currentLanguage === DEFAULT_LANGUAGE ? state.source : translateText(state.source, contextFor(node.parentElement, 'text'));
    state.rendered = next;
    if (node.nodeValue !== next) node.nodeValue = next;
  }

  function getAttrState(element) {
    var state = attrState.get(element);
    if (!state) { state = {}; attrState.set(element, state); }
    return state;
  }

  function translateAttribute(element, name) {
    if (!element.hasAttribute(name) || shouldSkip(element)) return;
    var allState = getAttrState(element);
    var value = element.getAttribute(name);
    var state = allState[name];
    if (!state || value !== state.rendered) {
      state = { source: value, rendered: value };
      allState[name] = state;
    }
    var next = currentLanguage === DEFAULT_LANGUAGE ? state.source : translateSource(state.source, contextFor(element, name));
    state.rendered = next;
    if (value !== next) element.setAttribute(name, next);
  }

  function translateElement(element) {
    if (!element || element.nodeType !== Node.ELEMENT_NODE || shouldSkip(element)) return;
    for (var a = 0; a < TRANSLATABLE_ATTRIBUTES.length; a++) translateAttribute(element, TRANSLATABLE_ATTRIBUTES[a]);
    for (var i = 0; i < element.childNodes.length; i++) {
      var child = element.childNodes[i];
      if (child.nodeType === Node.TEXT_NODE) translateTextNode(child);
      else if (child.nodeType === Node.ELEMENT_NODE) translateElement(child);
    }
  }

  function syncSelectors() {
    var selectors = document.querySelectorAll('[data-language-select]');
    for (var i = 0; i < selectors.length; i++) selectors[i].value = currentLanguage;
  }

  function applyDocument() {
    if (!document.documentElement || applying) return;
    applying = true;
    try {
      document.documentElement.lang = currentLanguage;
      translateElement(document.documentElement);
      syncSelectors();
    } finally { applying = false; }
  }

  function setLanguage(value) {
    var next = normaliseLanguage(value);
    if (SUPPORTED.indexOf(next) === -1) next = DEFAULT_LANGUAGE;
    currentLanguage = next;
    try { localStorage.setItem(STORAGE_KEY, next); } catch (_) {}
    applyDocument();
    syncObserver();
    document.dispatchEvent(new CustomEvent('mikrodash:languagechange', { detail: { language: next } }));
  }

  function bindSelectors() {
    document.addEventListener('change', function (event) {
      if (event.target && event.target.matches('[data-language-select]')) setLanguage(event.target.value);
    });
  }

  function processMutations(mutations) {
    if (applying) return;
    applying = true;
    try {
      for (var i = 0; i < mutations.length; i++) {
        var mutation = mutations[i];
        if (mutation.type === 'characterData') translateTextNode(mutation.target);
        if (mutation.type === 'attributes') translateAttribute(mutation.target, mutation.attributeName);
        for (var j = 0; mutation.addedNodes && j < mutation.addedNodes.length; j++) {
          var node = mutation.addedNodes[j];
          if (node.nodeType === Node.TEXT_NODE) translateTextNode(node);
          else if (node.nodeType === Node.ELEMENT_NODE) translateElement(node);
        }
      }
    } finally { applying = false; }
  }

  function startObserver() {
    if (currentLanguage === DEFAULT_LANGUAGE || !document.body || observer) return;
    observer = new MutationObserver(processMutations);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: TRANSLATABLE_ATTRIBUTES, childList: true, characterData: true, subtree: true });
  }

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function syncObserver() {
    if (currentLanguage === DEFAULT_LANGUAGE) stopObserver();
    else startObserver();
  }

  global.MikroDashI18n = {
    getLanguage: function () { return currentLanguage; },
    setLanguage: setLanguage,
    normaliseLanguage: normaliseLanguage,
    t: translateSource,
    apply: applyDocument,
    supportedLanguages: SUPPORTED.slice(),
    enableAudit: function (enabled) { auditEnabled = enabled !== false; },
    clearMisses: function () { misses = Object.create(null); },
    getMisses: function () { return Object.keys(misses).sort().map(function (key) { return misses[key]; }); },
  };

  bindSelectors();
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', function () { applyDocument(); syncObserver(); });
  else { applyDocument(); syncObserver(); }
})(window);
