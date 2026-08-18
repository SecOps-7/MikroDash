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

function createDom(language = 'en-US') {
  const dom = new JSDOM('<!doctype html><html><body><select data-language-select><option value="en-US">English</option><option value="zh-CN">简体中文</option></select><p id="label">Dashboard</p><input id="search" placeholder="Search"></body></html>', {
    url: 'http://127.0.0.1/', runScripts: 'dangerously', pretendToBeVisual: true,
  });
  Object.defineProperty(dom.window.navigator, 'language', { configurable: true, value: language });
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
  assert.equal(document.querySelector('#label').textContent, '仪表盘');
  assert.equal(document.querySelector('#search').placeholder, '搜索');
  MikroDashI18n.setLanguage('en-US');
  assert.equal(document.querySelector('#label').textContent, 'Dashboard');
  assert.equal(document.querySelector('#search').placeholder, 'Search');
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
  document.body.appendChild(button);
  await settle();
  assert.equal(button.textContent, '保存');
  assert.equal(button.title, '刷新');
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
    for (const url of ['/locales/', '/locales/private.js', '/locales/zh-CN.js.map', '/app.js']) {
      assert.equal((await get(base + url)).status, 401, url);
    }
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
