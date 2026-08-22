'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const vm = require('node:vm');
const express = require('express');
const { JSDOM } = require('jsdom');
const { isPublicI18nPath, PUBLIC_I18N_PATHS } = require('../src/i18nAssets');
const { audit } = require('../scripts/i18n-audit');

const root = path.resolve(__dirname, '..');
const readPublic = (name) => fs.readFileSync(path.join(root, 'public', name), 'utf8');

function loadLocale(name) {
  const window = {};
  vm.runInNewContext(readPublic(path.join('locales', name + '.js')), { window });
  return window.MikroDashLocales[name];
}

function createDom(language = 'en-US', options = {}) {
  const dom = new JSDOM('<!doctype html><html><body><h1 id="brand" data-i18n-skip>Mikro<span>Dash</span></h1><select data-language-select><option value="en-US">English</option><option value="zh-CN">简体中文</option></select><p id="label">Dashboard</p><input id="search" placeholder="Search"><img id="visual" alt="Dashboard" aria-description="Settings" aria-valuetext="Interfaces"></body></html>', {
    url: 'http://127.0.0.1/', runScripts: 'dangerously', pretendToBeVisual: true,
  });
  Object.defineProperty(dom.window.navigator, 'language', { configurable: true, value: language });
  if (options.trackObserver) {
    const NativeObserver = dom.window.MutationObserver;
    const stats = { created: 0, disconnected: 0 };
    dom.window.MutationObserver = function TrackingObserver(callback) {
      stats.created++;
      const instance = new NativeObserver(callback);
      const disconnect = instance.disconnect.bind(instance);
      instance.disconnect = function () { stats.disconnected++; return disconnect(); };
      return instance;
    };
    dom.observerStats = stats;
  }
  dom.window.eval(readPublic(path.join('locales', 'en-US.js')));
  dom.window.eval(readPublic(path.join('locales', 'zh-CN.js')));
  dom.window.eval(readPublic('i18n.js'));
  return dom;
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
function get(url) {
  return new Promise((resolve, reject) => {
    const request = http.get(url, { agent: false }, (response) => {
      response.resume();
      response.on('end', () => resolve({ status: response.statusCode, type: response.headers['content-type'] || '' }));
    });
    request.on('error', reject);
  });
}

test('locale maps required current navigation and settings copy', () => {
  const locale = loadLocale('zh-CN');
  for (const source of ['Dashboard', 'Interfaces', 'Settings', 'Interface Language', 'Routing', 'Wireless', 'Routers', 'Accounts']) {
    assert.notEqual(locale.messages[source], undefined, source);
    assert.notEqual(locale.messages[source], source, source);
  }
});

test('every static English UI candidate is translated or explicitly classified', () => {
  const result = audit();
  assert.deepEqual(result.missing, [], result.missing.join('\n'));
  assert.ok(Array.isArray(result.staleMessages));
  assert.ok(!result.staleMessages.includes('Dashboard'));
  assert.ok(!result.staleMessages.includes('This applies all scheduled package changes and REBOOTS the router.'));
});

test('English locale remains the source-language fallback', () => {
  const locale = loadLocale('en-US');
  assert.equal(locale.name, 'English');
  assert.deepEqual(Object.keys(locale.messages), []);
});

test('language normalisation selects only Simplified Chinese variants', () => {
  const dom = createDom();
  const normalise = dom.window.MikroDashI18n.normaliseLanguage;
  for (const value of ['zh', 'zh-CN', 'zh_CN', 'zh-SG', 'zh-Hans', 'zh-Hans-CN']) assert.equal(normalise(value), 'zh-CN', value);
  for (const value of ['zh-TW', 'zh-HK', 'zh-MO', 'zh-Hant', 'zh-Hant-TW', 'en-US']) assert.equal(normalise(value), 'en-US', value);
  dom.window.close();
});

test('language round-trip restores original text and attributes', async () => {
  const dom = createDom();
  const { document, MikroDashI18n } = dom.window;
  MikroDashI18n.setLanguage('zh-CN');
  assert.equal(document.querySelector('#brand').textContent, 'MikroDash');
  assert.equal(document.querySelector('#label').textContent, '仪表盘');
  assert.equal(document.querySelector('#search').placeholder, '搜索');
  assert.equal(document.querySelector('#visual').alt, '仪表盘');
  assert.equal(document.querySelector('#visual').getAttribute('aria-description'), '设置');
  assert.equal(document.querySelector('#visual').getAttribute('aria-valuetext'), '接口');
  MikroDashI18n.setLanguage('en-US');
  assert.equal(document.querySelector('#brand').textContent, 'MikroDash');
  assert.equal(document.querySelector('#label').textContent, 'Dashboard');
  assert.equal(document.querySelector('#search').placeholder, 'Search');
  assert.equal(document.querySelector('#visual').alt, 'Dashboard');
  assert.equal(document.querySelector('#visual').getAttribute('aria-description'), 'Settings');
  assert.equal(document.querySelector('#visual').getAttribute('aria-valuetext'), 'Interfaces');
  await settle();
  dom.window.close();
});

test('observer translates dynamic nodes and attributes without recursive corruption', async () => {
  const dom = createDom();
  const { document, MikroDashI18n } = dom.window;
  MikroDashI18n.setLanguage('zh-CN');
  const button = document.createElement('button');
  button.textContent = 'Save';
  button.title = 'Refresh';
  button.setAttribute('aria-description', 'Dashboard');
  document.body.appendChild(button);
  await settle();
  assert.equal(button.textContent, '保存');
  assert.equal(button.title, '刷新');
  assert.equal(button.getAttribute('aria-description'), '仪表盘');
  button.title = 'Search';
  await settle();
  assert.equal(button.title, '搜索');
  await settle();
  assert.equal(button.title, '搜索');
  dom.window.close();
});

test('explicit user-data boundary protects names, SSIDs and log text', async () => {
  const dom = createDom();
  const { document, MikroDashI18n } = dom.window;
  MikroDashI18n.setLanguage('zh-CN');
  const value = document.createElement('div');
  value.setAttribute('data-i18n-user-data', '');
  value.title = 'Save Router';
  value.textContent = 'Dashboard Home SSID';
  document.body.appendChild(value);
  await settle();
  assert.equal(value.textContent, 'Dashboard Home SSID');
  assert.equal(value.title, 'Save Router');
  dom.window.close();
});

test('dynamic patterns accept bounded UI values and reject arbitrary user data', () => {
  const dom = createDom();
  const i18n = dom.window.MikroDashI18n;
  i18n.setLanguage('zh-CN');
  assert.equal(i18n.t('3 devices'), '3 台设备');
  assert.equal(i18n.t('5 minutes ago'), '5 分钟前');
  assert.equal(i18n.t('Connected to My Router'), 'Connected to My Router');
  assert.equal(i18n.t('No Dashboard Home SSID found'), 'No Dashboard Home SSID found');
  assert.equal(i18n.t('Save Alice'), 'Save Alice');
  dom.window.close();
});

test('application marks router and user values as translation boundaries', () => {
  const app = readPublic('app.js');
  assert.match(app, /<tr data-i18n-user-data><td>'\+esc\(d\.name/,
    'Top Talker device names and MAC addresses are router data');
  assert.match(app, /log-line" data-i18n-user-data/,
    'RouterOS log topics and messages are router data');
  assert.match(app, /wl-ssid-name" data-i18n-user-data/,
    'SSID values are router data');
  assert.match(app, /opt\.setAttribute\('data-i18n-user-data', ''\)/,
    'interface names in the traffic selector are router data');
  assert.match(app, /data-i18n-user-data>' \+ esc\(u\.username\)/,
    'account names are user data');
});

test('mutation observer only runs while a translated language is active', async () => {
  const dom = createDom('en-US', { trackObserver: true });
  await settle();
  assert.equal(dom.observerStats.created, 0, 'English should not pay for a document-wide observer');
  dom.window.MikroDashI18n.setLanguage('zh-CN');
  assert.equal(dom.observerStats.created, 1);
  dom.window.MikroDashI18n.setLanguage('en-US');
  assert.equal(dom.observerStats.disconnected, 1);
  dom.window.MikroDashI18n.setLanguage('zh-CN');
  assert.equal(dom.observerStats.created, 2, 'switching back to Chinese re-arms translation');
  dom.window.close();
});

test('every page introduced by upstream v0.7.25 has a translated document title', () => {
  const messages = loadLocale('zh-CN').messages;
  const pages = ['VLANs', 'PPP', 'Bridges', 'DNS', 'CAPsMAN', 'Packages',
    'Queues', 'Router Users', 'WAN', 'Audit'];
  const translatedWords = new Set(['VLANs', 'Bridges', 'Packages', 'Queues', 'Router Users', 'Audit']);
  for (const page of pages) {
    const source = 'MikroDash — ' + page;
    assert.ok(messages[source], source);
    if (translatedWords.has(page)) assert.notEqual(messages[source], source, source);
  }
});

test('v0.7.25 router-write confirmations and refusals are translated', () => {
  const messages = loadLocale('zh-CN').messages;
  const required = [
    'This applies all scheduled package changes and REBOOTS the router.',
    'The uplink goes down until the client rebinds — usually seconds, but it is a real outage.',
    'Traffic it was limiting will no longer be shaped.',
    'They will no longer be able to log in to this router.',
    'Package collection is not running for this router',
    'WAN collection is not running for this router',
    'Queue collection is not running for this router',
    'Router user collection is not running for this router',
    'That is the account MikroDash signs in with — manage it in WinBox',
    'The RouterOS user needs the "policy" permission for this',
  ];
  for (const source of required) {
    assert.ok(messages[source], source);
    assert.notEqual(messages[source], source, source);
  }
  const app = readPublic('app.js');
  assert.match(app, /function tr\(s, context\)/);
  assert.match(app, /window\.prompt\(\s*tr\(/);
  assert.match(app, /window\.confirm\(tr\('Remove the queue'/);
  assert.match(app, /'user-remove':\s+tr\('Remove the router user'/);
});

test('audit mode records inspectable misses without changing the display', () => {
  const dom = createDom();
  const i18n = dom.window.MikroDashI18n;
  i18n.setLanguage('zh-CN');
  i18n.enableAudit(true);
  assert.equal(i18n.t('Future Feature Label', 'test'), 'Future Feature Label');
  assert.deepEqual(JSON.parse(JSON.stringify(i18n.getMisses())), [{ text: 'Future Feature Label', count: 1, contexts: ['test'] }]);
  i18n.clearMisses();
  assert.deepEqual(JSON.parse(JSON.stringify(i18n.getMisses())), []);
  dom.window.close();
});

test('application and login pages load locales before page scripts', () => {
  const index = readPublic('index.html');
  const login = readPublic('login.html');
  assert.ok(index.indexOf('/locales/zh-CN.js') < index.indexOf('/i18n.js'));
  assert.ok(index.indexOf('/i18n.js') < index.indexOf('/app.js'));
  assert.ok(login.indexOf('/locales/zh-CN.js') < login.indexOf('/i18n.js'));
  assert.ok(login.indexOf('/i18n.js') < login.indexOf('/login.js'));
  assert.match(index, /data-language-select/);
  assert.match(index, /id="topbarLogo"\s+data-i18n-skip>Mikro<span>Dash<\/span>/,
    'the split brand is an explicit translation boundary');
  assert.match(login, /data-language-select/);
});

test('signed-out HTTP surface exposes exactly three i18n assets', async () => {
  assert.deepEqual([...PUBLIC_I18N_PATHS].sort(), ['/i18n.js', '/locales/en-US.js', '/locales/zh-CN.js']);
  const app = express();
  app.use((req, res, next) => isPublicI18nPath(req.path) ? next() : res.status(401).send('sign in'));
  app.use(express.static(path.join(root, 'public')));
  const server = http.createServer(app);
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  try {
    for (const url of ['/i18n.js', '/locales/en-US.js', '/locales/zh-CN.js']) {
      const response = await get(base + url);
      assert.equal(response.status, 200, url);
      assert.match(response.type, /javascript/, url);
    }
    for (const url of ['/locales/', '/locales/private.js', '/locales/zh-CN.js.map',
      '/locales/../app.js', '/locales/%2e%2e/app.js', '/app.js']) {
      assert.equal((await get(base + url)).status, 401, url);
    }
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
