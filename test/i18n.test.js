'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const vm = require('node:vm');
const express = require('express');
const { JSDOM } = require('jsdom');
const espree = require('espree');
const { isPublicI18nPath, PUBLIC_I18N_PATHS } = require('../src/i18nAssets');
const { audit } = require('../scripts/i18n-audit');

const root = path.resolve(__dirname, '..');
const readPublic = (name) => fs.readFileSync(path.join(root, 'public', name), 'utf8');
const appSource = readPublic('app.js');
const appAst = espree.parse(appSource, { ecmaVersion: 2022, sourceType: 'script', range: true });

function walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  visit(node);
  for (const value of Object.values(node)) {
    if (Array.isArray(value)) value.forEach((item) => walk(item, visit));
    else if (value && typeof value.type === 'string') walk(value, visit);
  }
}

function appFunction(name, marker = '') {
  const matches = [];
  walk(appAst, (node) => {
    if (node.type !== 'FunctionDeclaration' || !node.id || node.id.name !== name) return;
    const source = appSource.slice(node.range[0], node.range[1]);
    if (!marker || source.includes(marker)) matches.push(source);
  });
  assert.equal(matches.length, 1, `expected one ${name} renderer containing ${marker || '(any source)'}`);
  return matches[0];
}

function appFunctions(specs, bindings, returned) {
  const declarations = specs.map(([name, marker]) => appFunction(name, marker)).join('\n');
  const names = Object.keys(bindings);
  return Function(...names, `'use strict';\n${declarations}\nreturn ${returned};`)(
    ...names.map((name) => bindings[name])
  );
}

function escapeHtml(value) {
  return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#039;');
}

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
  assert.equal(i18n.t('just now'), '刚刚');
  assert.equal(i18n.t('5m ago'), '5 分钟前');
  assert.equal(i18n.t('2h ago'), '2 小时前');
  assert.equal(i18n.t('3d ago'), '3 天前');
  assert.equal(i18n.t('1 network'), '1 个网络');
  assert.equal(i18n.t('4 networks'), '4 个网络');
  assert.equal(i18n.t('2 routers'), '2 台路由器');
  assert.equal(i18n.t('1 offline'), '1 台离线');
  assert.equal(i18n.t('Connected to My Router'), 'Connected to My Router');
  assert.equal(i18n.t('Last updated Dashboard'), 'Last updated Dashboard');
  assert.equal(i18n.t('Updated Admin'), 'Updated Admin');
  assert.equal(i18n.t('Last handshake Down'), 'Last handshake Down');
  assert.equal(i18n.t('Connected to router: Viewer'), 'Connected to router: Viewer');
  assert.equal(i18n.t('Switching to router: Unknown…'), 'Switching to router: Unknown…');
  assert.equal(i18n.t('No Dashboard Home SSID found'), 'No Dashboard Home SSID found');
  assert.equal(i18n.t('Save Alice'), 'Save Alice');
  dom.window.close();
});

test('application marks router and user values as translation boundaries', () => {
  const app = readPublic('app.js');
  const index = readPublic('index.html');
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
  assert.match(app, /class="top-name"[^>]+data-i18n-user-data[^>]*>'\+esc\(s\.name\)/,
    'connection source names are router data');
  assert.match(app, /class="ifl-name" data-i18n-user-data title=/,
    'interface names and comments are router data');
  assert.match(app, /class="rtr-dd-name" data-i18n-user-data/,
    'router picker labels are user data');
  assert.match(app, /esc\(r\.chain\).*data-i18n-user-data|data-i18n-user-data>'\+esc\(r\.chain\)/,
    'firewall chains are RouterOS data');
  assert.match(app, /esc\(r\.comment \|\| '—'\).*data-i18n-user-data|data-i18n-user-data>' \+ esc\(r\.comment/,
    'route comments are RouterOS data');
  assert.match(app, /<td data-i18n-user-data>' \+ esc\(u\.name\)/,
    'RouterOS usernames are router data');
  assert.match(app, /<td data-i18n-user-data>' \+ \(r\.target/,
    'audit targets are recorded data');
  assert.match(app, /class="wn-ssid-pill" data-i18n-user-data/,
    'Wi-Fi SSIDs are router data');
  assert.match(index, /id="authUsername" data-i18n-user-data/,
    'the signed-in application username is user data');
  assert.match(index, /id="routerSelectLabel" data-i18n-user-data/,
    'the selected router label is user data');
});

test('representative live values stay intact while adjacent UI translates', async () => {
  const dom = createDom();
  const { document, MikroDashI18n } = dom.window;
  const fixture = document.createElement('section');
  fixture.innerHTML = [
    '<span>Interfaces</span><span data-i18n-user-data>Bridge</span>',
    '<span>Devices</span><span data-i18n-user-data>Unknown</span>',
    '<span>Wireless</span><span data-i18n-user-data>Down</span>',
    '<span>Accounts</span><span data-i18n-user-data>Admin</span>',
    '<span>Router Users</span><span data-i18n-user-data>Viewer</span>',
    '<span>Logs</span><span data-i18n-user-data>Save Alice</span>',
    '<span>Audit</span><span data-i18n-user-data>Dashboard</span>',
  ].join('');
  document.body.appendChild(fixture);
  MikroDashI18n.setLanguage('zh-CN');
  await settle();
  const values = [...fixture.querySelectorAll('[data-i18n-user-data]')].map((node) => node.textContent);
  assert.deepEqual(values, ['Bridge', 'Unknown', 'Down', 'Admin', 'Viewer', 'Save Alice', 'Dashboard']);
  assert.equal(fixture.firstElementChild.textContent, '接口');
  assert.notEqual(fixture.querySelectorAll('span')[4].textContent, 'Wireless');
  dom.window.close();
});

test('production renderers preserve collision-prone live values in Chinese mode', async () => {
  const dom = createDom();
  const { document, MikroDashI18n } = dom.window;
  MikroDashI18n.setLanguage('zh-CN');

  const wirelessTable = document.createElement('tbody');
  document.body.appendChild(wirelessTable);
  const renderWireless = appFunctions([
    ['renderWireless', 'wl-group-label'],
  ], {
    wirelessTable,
    _renderSortHeader() {},
    _wlSyncSortBtns() {},
    _wlClients: [
      { iface: 'Bridge', ssid: 'Total', name: 'Admin', mac: 'Unknown', ip: 'Dashboard', signal: '-50', txRate: '1 Mbps', rxRate: '2 Mbps', uptime: 'Viewer', source: 'capsman', band: '5 GHz' },
      { iface: 'ether2', ssid: 'Guest', name: 'client-2', mac: '00:11:22:33:44:55', ip: '192.0.2.2', signal: '-60', txRate: '0 Mbps', rxRate: '0 Mbps', uptime: '1m', source: 'local', band: '5 GHz' },
    ],
    _wlSortState: { col: 'signal', dir: 'desc' },
    sortClients: (rows) => rows.slice(),
    $: () => null,
    esc: escapeHtml,
    signalBars: () => '',
    parseTxRateNum: () => 0,
    parseTxRate: (value) => value,
    sigQuality: () => 'Excellent',
    bandBadge: (value) => `<span>${escapeHtml(value)}</span>`,
  }, 'renderWireless');
  renderWireless();

  const notifList = document.createElement('div');
  notifList.id = 'notifList';
  document.body.appendChild(notifList);
  const renderNotifPanel = appFunctions([
    ['dataText', 'data-i18n-user-data'],
    ['_alertAgeStr', 'just now'],
    ['renderNotifPanel', 'notif-item-title'],
  ], {
    $: (id) => document.getElementById(id),
    _alerts: [{ id: 'a1', label: 'Alerts', subject: 'Unknown', detail: 'Save Alice', routerName: 'Dashboard', firedAt: Date.now() }],
    _alertIsOpen: () => true,
    esc: escapeHtml,
  }, 'renderNotifPanel');
  renderNotifPanel();

  const schedBody = document.createElement('tbody');
  schedBody.id = 'rptSchedTbody';
  const schedActions = document.createElement('div');
  schedActions.id = 'rptSchedActions';
  const schedNotice = document.createElement('div');
  schedNotice.id = 'rptSchedNotice';
  const schedNoticeText = document.createElement('span');
  schedNoticeText.id = 'rptSchedNoticeText';
  document.body.append(schedBody, schedActions, schedNotice, schedNoticeText);
  const renderSchedules = appFunctions([
    ['dataText', 'data-i18n-user-data'],
    ['scheduleFrequency', "hourly: 'Hourly'"],
    ['renderSchedules', 'rptSchedTbody'],
  ], {
    $: (id) => document.getElementById(id),
    _sched: {
      permitted: true, smtpReady: true,
      rows: [{ id: 's1', name: 'Dashboard', enabled: false, disabledReason: 'Unknown', frequency: 'daily', sendHour: 7, sections: ['ping'], iface: 'Bridge', recipients: ['ops@example.com'], lastRun: null }],
    },
    esc: escapeHtml,
    fmtRun: () => '—',
  }, 'renderSchedules');
  renderSchedules();

  dom.window._caps = { routers: { manageable: [] } };
  const mapRenderers = appFunctions([
    ['dataText', 'data-i18n-user-data'],
    ['popHtml', 'rmp-grid'],
    ['groupPopHtml', 'rmp-list'],
  ], { window: dom.window, esc: escapeHtml }, '({ popHtml, groupPopHtml })');
  const groupPopover = document.createElement('div');
  groupPopover.innerHTML = mapRenderers.groupPopHtml({ routers: [
    { id: 'r1', label: 'Admin', host: 'Bridge', connected: true, geo: { label: 'Dashboard' } },
    { id: 'r2', label: 'Viewer', host: 'Unknown', connected: false, geo: { label: 'Dashboard' } },
  ] });
  document.body.appendChild(groupPopover);
  const singlePopover = document.createElement('div');
  singlePopover.innerHTML = mapRenderers.popHtml({
    id: 'r3', label: 'Admin', host: 'Bridge', connected: true, uptime: 'Viewer',
    geo: { label: 'Dashboard', source: 'manual' },
  });
  document.body.appendChild(singlePopover);

  const pppTable = document.createElement('tbody');
  pppTable.id = 'pppServerTable';
  document.body.appendChild(pppTable);
  const renderPppConfig = appFunctions([
    ['dataText', 'data-i18n-user-data'],
    ['renderConfig', 'pppServerTable'],
  ], {
    $: (id) => document.getElementById(id),
    _data: {
      servers: [{ serviceName: 'Bridge', interface: 'Admin', maxSessions: 5, disabled: false }],
      profiles: [{ name: 'Viewer', localAddress: 'Dashboard', rateLimit: 'Unknown', onlyOne: 'Down' }],
    },
    esc: escapeHtml,
  }, 'renderConfig');
  renderPppConfig();

  const alertTable = document.createElement('tbody');
  document.body.appendChild(alertTable);
  const applyAlertSort = appFunctions([
    ['dataText', 'data-i18n-user-data'],
    ['_applyAlertSort', 'acknowledged_by'],
  ], {
    _alertRawRows: [{ id: 1, fired_at: 1, resolved_at: 2, alert_label: 'Alerts', alert_type: 'test', subject: 'Unknown', detail: 'Save Alice', acknowledged_at: 3, acknowledged_by: 'Admin' }],
    _alertSort: { col: 'fired_at', dir: 'desc' },
    _sortRows: (rows) => rows,
    rptAlertTbody: alertTable,
    _renderSortHeader() {},
    fmtTs: (value) => String(value),
    fmtDuration: () => 'Viewer',
    esc: escapeHtml,
  }, '_applyAlertSort');
  applyAlertSort();

  await settle();
  assert.equal(wirelessTable.querySelector('.wl-group-label').textContent, 'Bridge');
  assert.match(wirelessTable.textContent, /1 个客户端/);
  assert.equal(notifList.querySelector('.notif-item-title > span').textContent, '告警');
  assert.deepEqual(
    [...notifList.querySelectorAll('[data-i18n-user-data]')].map((node) => node.textContent),
    ['Unknown', 'Save Alice', 'Dashboard']
  );
  assert.match(notifList.textContent, /刚刚/);
  assert.ok([...schedBody.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === 'Dashboard'));
  assert.ok([...schedBody.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === 'Bridge'));
  assert.match(schedBody.textContent, /每天/);
  assert.match(groupPopover.textContent, /2 台路由器/);
  assert.match(groupPopover.textContent, /1 台离线/);
  assert.deepEqual([...groupPopover.querySelectorAll('.rmp-rl')].map((node) => node.textContent), ['Admin', 'Viewer']);
  assert.equal(singlePopover.querySelector('.rmp-name [data-i18n-user-data]').textContent, 'Admin');
  for (const value of ['Bridge', 'Admin', 'Viewer', 'Dashboard', 'Unknown', 'Down']) {
    assert.ok([...pppTable.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === value), value);
  }
  assert.equal(pppTable.querySelector('.wl-band').textContent, '服务器');
  assert.ok([...alertTable.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === 'Unknown'));
  assert.ok([...alertTable.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === 'Save Alice'));
  assert.ok([...alertTable.querySelectorAll('[data-i18n-user-data]')].some((node) => node.textContent === 'Admin'));
  dom.window.close();
});

test('audit rejects unresolved translation calls and untranslated native dialogs', () => {
  const result = audit();
  assert.deepEqual(result.unresolvedTranslations, [], result.unresolvedTranslations.join('\n'));
  assert.deepEqual(result.untranslatedDialogs, [], result.untranslatedDialogs.join('\n'));
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

test('production authentication middleware uses the exact i18n asset helper', () => {
  const server = fs.readFileSync(path.join(root, 'src', 'index.js'), 'utf8');
  assert.match(server, /const \{ isPublicI18nPath \} = require\('\.\/i18nAssets'\);/);
  assert.match(server, /_isPublicPath\(req\.path\) \|\| isPublicI18nPath\(req\.path\) \|\| req\.path\.startsWith\('\/vendor\/'\)/);
  assert.doesNotMatch(server, /req\.path\.startsWith\('\/locales\/'\)/,
    'production auth must not expose the whole locale directory');
});
