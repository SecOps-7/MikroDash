const test = require('node:test');
const assert = require('node:assert/strict');

const { parseBps, bpsToMbps } = require('../src/collectors/traffic');

test('parseBps handles raw integer strings from RouterOS binary API', () => {
  assert.equal(parseBps('27800'), 27800);
  assert.equal(parseBps('1500000'), 1500000);
  assert.equal(parseBps('0'), 0);
});

test('parseBps handles kbps/Mbps/Gbps suffixed values', () => {
  assert.equal(parseBps('27.8kbps'), 27800);
  assert.equal(parseBps('27.8Kbps'), 27800);
  assert.equal(parseBps('1.5Mbps'), 1500000);
  assert.equal(parseBps('1.5mbps'), 1500000);
  assert.equal(parseBps('2.1Gbps'), 2100000000);
  assert.equal(parseBps('2.1gbps'), 2100000000);
});

test('parseBps handles plain bps suffix and edge cases', () => {
  assert.equal(parseBps('500bps'), 500);
  assert.equal(parseBps(undefined), 0);
  assert.equal(parseBps(null), 0);
  assert.equal(parseBps(''), 0);
});

test('bpsToMbps converts and rounds to 3 decimal places', () => {
  assert.equal(bpsToMbps(27800), 0.028);
  assert.equal(bpsToMbps(1500000), 1.5);
  assert.equal(bpsToMbps(0), 0);
  assert.equal(bpsToMbps(undefined), 0);
  assert.equal(bpsToMbps(null), 0);
});
