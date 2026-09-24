/**
 * MERGING THE FLEET'S NEIGHBOUR TABLES INTO ONE GRAPH.
 *
 * ── WHAT WAS ASKED FOR ──────────────────────────────────────────────────────
 *
 * "Rebuild the map by adding all the access points and merging into one." The
 * graph is built from one router's tables, so a device on an access point's own
 * port is not on it at all, and a device the core heard through a switch is on
 * it in the wrong place.
 *
 * ── THE RULE IS THE UPLINK, AND THAT IS THE ONLY THING TESTED HERE ──────────
 *
 * A peer's uplink is the interface it sees the viewed router on. Anything it
 * reports on a DIFFERENT interface is behind it; anything on the uplink is
 * beside it. Four cases separate a correct implementation from the three wrong
 * ones that look right on a single fixture:
 *
 *	a device only the peer can see          added, behind the peer
 *	a device the peer sees on another port  moved behind the peer
 *	a device the peer sees on its uplink    left exactly where it was
 *	a peer nothing on the map has heard of  added, so its devices have a parent
 *	a peer that could not be read           counted, not silently dropped
 *
 * The flat-segment case is the third one: every router sees every other on its
 * uplink, so the merge moves nothing. That is the honest answer and it is pinned
 * here so a later "improvement" that guesses cannot land quietly.
 *
 * ── THE CORE NODE CARRIES NO MAC, AND THIS TEST USED TO PRETEND IT DID ─────
 *
 * `BuildTopology` builds the core with neither a MAC nor an identity - it has no
 * source for either - so a fixture that set one made every case below pass while
 * production took the opposite branch: the uplink set was empty, everything read
 * as "behind" the peer, and the peer's row for the viewed router was added as a
 * second copy of it. The addresses now come in as `selfMacs`, `base()` builds a
 * core with `mac: ''` the way the collector does, and the last case pins what
 * happens when they are missing.
 */
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.tpm-entry.ts');
fs.writeFileSync(ENTRY, "export { mergePeers } from '../web/src/pages/topo-merge.js';\n");
const OUT = path.join(ROOT, 'testdata', '.tpm.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const { mergePeers } = require(OUT);

const CORE_MAC = '02:00:00:00:00:01';
const AP_MAC = '02:00:00:00:00:02';
const SWITCH_MAC = '02:00:00:00:00:03';
const CAM_MAC = '02:00:00:00:00:04';
const FAR_MAC = '02:00:00:00:00:05';

function node(over: Record<string, unknown>): Record<string, unknown> {
  return Object.assign({
    key: '', kind: 'neighbor', name: '', identity: '', mac: '', ip: '', ip6: '',
    type: 'other', typeSource: 'unknown', caps: [], capsEnabled: [], platform: '',
    board: '', version: '', softwareId: '', description: '', uptime: '', ageSec: null,
    via: ['mndp'], running: [], ifaces: [], remoteIface: '', ipv6: false, gone: false,
    firstSeen: 0, lastSeen: 0, rtt: null, loss: null, pingTs: null, status: 'unknown',
    port: '', parent: null, pinned: false, clientCount: 0,
  }, over);
}

/** The viewed router: an access point and a switch, both on ether8. */
function base(): Record<string, unknown> {
  return {
    ts: 0, routerId: 'a', pollMs: 5000, discovery: null, permissionDenied: false,
    pingDenied: false, neighborCount: 2, pinsEnabled: true, vlans: [],
    clientCount: 0, clientsTruncated: 0,
    nodes: [
      // NO MAC AND NO IDENTITY, as BuildTopology writes it.
      node({ key: 'core', kind: 'core', name: 'gw' }),
      node({ key: AP_MAC, name: 'ap-yard', mac: AP_MAC, ifaces: ['ether8'], port: 'ether8' }),
      node({ key: SWITCH_MAC, name: 'swos', mac: SWITCH_MAC, ifaces: ['ether8'], port: 'ether8' }),
    ],
    edges: [
      { id: 'ether8|' + AP_MAC, from: 'core', to: AP_MAC, iface: 'ether8', viaPort: 'ether8',
        remoteIface: '', shared: true, inferred: false, pinned: false, gone: false },
      { id: 'ether8|' + SWITCH_MAC, from: 'core', to: SWITCH_MAC, iface: 'ether8', viaPort: 'ether8',
        remoteIface: '', shared: true, inferred: false, pinned: false, gone: false },
    ],
  };
}

/** The access point, answering for itself. */
function apPeer(neighbors: Array<Record<string, unknown>>): Record<string, unknown> {
  return {
    id: 'b', label: 'ap-yard', ok: true, error: '', macs: [AP_MAC],
    neighbors: neighbors.map((n) => node(n)),
  };
}

const find = (m: { nodes: Array<Record<string, unknown>> }, key: string) =>
  m.nodes.find((n) => n.key === key);

// ── 1. a device only the peer can see is added behind it ───────────────────
{
  const m = mergePeers(base(), [apPeer([
    { key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'], name: 'gw' },
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'], name: 'cam-gate' },
  ])], [CORE_MAC], 0);
  const cam = find(m, CAM_MAC);
  assert.ok(cam, 'the camera only the access point can see was not added');
  assert.strictEqual(cam!.parent, AP_MAC,
    'it was added, but not behind the access point that reported it');
  assert.strictEqual(m.added, 1, 'added = ' + m.added);
  assert.strictEqual(m.owner[CAM_MAC], 'ap-yard', 'the panel cannot say who reported it');
  assert.ok(m.edges.some((e: { from: string; to: string; inferred: boolean }) =>
    e.from === AP_MAC && e.to === CAM_MAC && e.inferred),
  'no inferred link was drawn from the access point to the camera');
  say('ok  a device only a peer can see is added behind that peer');
}

// ── 2. a device the peer sees on another port is MOVED behind it ───────────
{
  const b = base();
  (b.nodes as Array<Record<string, unknown>>).push(
    node({ key: CAM_MAC, name: 'cam-gate', mac: CAM_MAC, ifaces: ['ether8'], port: 'ether8' }));
  (b.edges as Array<Record<string, unknown>>).push(
    { id: 'ether8|' + CAM_MAC, from: 'core', to: CAM_MAC, iface: 'ether8', viaPort: 'ether8',
      remoteIface: '', shared: true, inferred: false, pinned: false, gone: false });

  const m = mergePeers(b, [apPeer([
    { key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'] },
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'] },
  ])], [CORE_MAC], 0);
  assert.strictEqual(find(m, CAM_MAC)!.parent, AP_MAC,
    'a device the peer reports on ether3 was left hanging off the core');
  assert.strictEqual(m.moved, 1, 'moved = ' + m.moved);
  assert.strictEqual(m.added, 0, 'a device already on the map was counted as added');
  const toCam = m.edges.filter((e: { to: string; client?: boolean }) => e.to === CAM_MAC);
  assert.strictEqual(toCam.length, 1,
    'the old link from the core survived the move: ' + JSON.stringify(toCam));
  assert.strictEqual(toCam[0].from, AP_MAC, 'the surviving link is the wrong one');
  say('ok  a device a peer reports off its uplink is moved behind that peer');
}

// ── 3. a flat segment moves nothing, and says so ───────────────────────────
//
// The switch forwards the discovery protocols, so the access point sees the
// core, the switch AND the camera on its one uplink port. Nothing in that says
// which of them is nearer, and the merge must not pretend otherwise.
{
  const b = base();
  (b.nodes as Array<Record<string, unknown>>).push(
    node({ key: CAM_MAC, name: 'cam-gate', mac: CAM_MAC, ifaces: ['ether8'], port: 'ether8' }));

  const m = mergePeers(b, [apPeer([
    { key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'] },
    { key: SWITCH_MAC, mac: SWITCH_MAC, ifaces: ['ether1'] },
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether1'] },
  ])], [CORE_MAC], 0);
  assert.strictEqual(m.moved, 0, 'a flat segment was rearranged: moved = ' + m.moved);
  assert.strictEqual(m.added, 0, 'a flat segment invented a device: added = ' + m.added);
  assert.strictEqual(find(m, CAM_MAC)!.parent, null,
    'the camera was re-parented on evidence that does not exist');
  assert.strictEqual(m.answered, 1, 'the peer was not counted as having answered');
  assert.strictEqual(m.failed, 0, 'a peer that answered was counted as unreachable');
  say('ok  a flat segment is left alone, because nothing in it says otherwise');
}

// ── 4. a peer nothing on the map has heard of still lands on it ────────────
{
  const m = mergePeers(base(), [{
    id: 'c', label: 'shed-ap', ok: true, error: '', macs: [FAR_MAC],
    neighbors: [
      node({ key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'] }),
      node({ key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether2'] }),
    ],
  }], [CORE_MAC], 0);
  const far = find(m, FAR_MAC);
  assert.ok(far, 'a managed router the core cannot hear was dropped');
  assert.strictEqual(far!.name, 'shed-ap', 'it has no name to draw');
  assert.strictEqual(find(m, CAM_MAC)!.parent, FAR_MAC,
    'its own device did not hang off it');
  assert.strictEqual(m.added, 2, 'added = ' + m.added);
  say('ok  a peer the viewed router cannot hear is added, with its devices');
}

// ── 5. a router that could not be read is counted, not dropped ────────────
//
// Silently skipping it reads as a router that sees nothing, which is the
// opposite of the truth.
{
  const m = mergePeers(base(), [
    { id: 'b', label: 'ap-yard', ok: false, error: 'unreachable', macs: [], neighbors: [] },
  ], [CORE_MAC], 0);
  assert.strictEqual(m.failed, 1, 'failed = ' + m.failed);
  assert.strictEqual(m.answered, 0, 'a peer that failed was counted as having answered');
  assert.strictEqual(m.added, 0, 'a peer that failed still put something on the map');
  say('ok  a peer that could not be read is counted rather than dropped');
}

// ── 6. the viewed router's own answer is not merged into its own graph ─────
{
  const m = mergePeers(base(), [{
    id: 'a', label: 'gw', ok: true, error: '', macs: [CORE_MAC],
    neighbors: [node({ key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'] })],
  }], [CORE_MAC], 0);
  assert.strictEqual(m.answered, 0, 'the viewed router was merged into itself');
  assert.strictEqual(m.added, 0, 'and it added its own neighbours a second time');
  say('ok  the router being viewed is skipped: it is already the graph');
}

// ── 7. a pin outranks anything read off a peer ─────────────────────────────
{
  const b = base();
  (b.nodes as Array<Record<string, unknown>>).push(
    node({ key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether8'], parent: SWITCH_MAC, pinned: true }));

  const m = mergePeers(b, [apPeer([
    { key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'] },
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'] },
  ])], [CORE_MAC], 0);
  assert.strictEqual(find(m, CAM_MAC)!.parent, SWITCH_MAC,
    'the merge overrode a link the operator pinned by hand');
  assert.strictEqual(m.moved, 0, 'moved = ' + m.moved);
  say('ok  a pinned link is the operator’s and the merge leaves it alone');
}

// ── 8. two peers claiming each other do not produce a cycle ────────────────
{
  const m = mergePeers(base(), [
    { id: 'b', label: 'ap-yard', ok: true, error: '', macs: [AP_MAC],
      neighbors: [node({ key: SWITCH_MAC, mac: SWITCH_MAC, ifaces: ['ether3'] })] },
    { id: 'c', label: 'swos-rb', ok: true, error: '', macs: [SWITCH_MAC],
      neighbors: [node({ key: AP_MAC, mac: AP_MAC, ifaces: ['ether3'] })] },
  ], [CORE_MAC], 0);
  const walk = (key: string): number => {
    const seen = new Set<string>();
    let cur = find(m, key);
    let hops = 0;
    while (cur && cur.parent && hops < 50) {
      assert.ok(!seen.has(cur.parent as string), 'the merged graph has a cycle');
      seen.add(cur.parent as string);
      cur = find(m, cur.parent as string);
      hops++;
    }
    return hops;
  };
  walk(AP_MAC);
  walk(SWITCH_MAC);
  say('ok  two peers claiming each other leave a tree, not a loop');
}

// ── 9. with no addresses for the viewed router, nothing is merged ────────
//
// The endpoint could not read them - the router went away, or the grant is gone.
// Guessing without them is what the flat-segment rule exists to prevent, so the
// map is left exactly as the collector built it.
{
  const m = mergePeers(base(), [apPeer([
    { key: CORE_MAC, mac: CORE_MAC, ifaces: ['ether1'] },
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'] },
  ])], [], 0);
  assert.strictEqual(m.added, 0, 'a device was invented with nothing to measure against');
  assert.strictEqual(m.moved, 0, 'the graph was rearranged with nothing to measure against');
  assert.strictEqual(m.answered, 0, 'a peer was counted as merged');
  assert.strictEqual(m.nodes.length, base().nodes.length, 'the node list changed');
  say('ok  with no addresses for the viewed router the map is left alone');
}

// ── 10. a peer that cannot see the viewed router still contributes ───────
//
// No uplink to measure against, so nothing MOVES - but a device only that peer
// can see is still a device only that peer can see.
{
  const m = mergePeers(base(), [apPeer([
    { key: CAM_MAC, mac: CAM_MAC, ifaces: ['ether3'], name: 'cam-gate' },
  ])], [CORE_MAC], 0);
  assert.strictEqual(m.added, 1, 'added = ' + m.added);
  assert.strictEqual(find(m, CAM_MAC)!.parent, AP_MAC, 'it did not hang off the peer');
  assert.strictEqual(m.moved, 0, 'a peer with no visible uplink rearranged the graph');
  say('ok  a peer that cannot see the viewed router adds but never moves');
}

fs.rmSync(OUT, { force: true });
say('topo-merge: all checks passed');
