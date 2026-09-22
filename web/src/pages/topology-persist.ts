// The Network Topology page's persistence: the rest of the fleet merged in on
// demand, and the operator's own cabling pins, per router. Split out of
// topology.ts; it reads and writes the page's shared state (TopoState) and
// reaches the rest of the page through `deps`.

import { el as byId } from '../dom';
import { t } from '../i18n';
import { mergePeers } from './topo-merge';
import type { RouterRecord } from '../events-hand';
import type { TopoState } from './topology';

export function topoPersist(st: TopoState, deps: {
  render(): void;
  renderPanel(): void;
}): {
  applyData(): void; loadPeers(): void; syncFleetBtn(): void;
  loadPins(): void; savePins(): void; syncPinsBtn(): void;
} {
  // ── the rest of the fleet ────────────────────────────────────────────
  //
  // One router's graph is one router's horizon. With the switch on, every other
  // router the operator added is read once and folded in — see pages/topo-merge.ts
  // for the rule and for what it deliberately does not claim.
  //
  // READ ON DEMAND. The payload on the socket stays the live one; the merge is
  // recomputed from it on every tick, against peer tables that are a snapshot.


  function applyData(): void {
    if (!st.livePayload || !st.fleetOn || !st.peers.length) {
      st.data = st.livePayload;
      st.fleetStat = { added: 0, moved: 0, answered: 0, failed: 0 };
      st.fleetOwner = {};
      return;
    }
    const m = mergePeers(st.livePayload, st.peers, st.selfMacs, Date.now());
    st.data = { ...st.livePayload, nodes: m.nodes, edges: m.edges };
    st.fleetStat = { added: m.added, moved: m.moved, answered: m.answered, failed: m.failed };
    st.fleetOwner = m.owner;
  }

  /** The fleet, fetched. `routers:update` is a CHANGE notification and is not
   *  sent on connect, so waiting for it leaves this with nothing to merge. */
  function ensureFleet(): Promise<void> {
    if (st.fleetRouters.length) return Promise.resolve();
    return fetch('/api/routers', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => { st.fleetRouters = ((j && j.routers) || []) as RouterRecord[]; })
      .catch(() => { /* nothing to merge is a working state */ });
  }

  function loadPeers(): void {
    void ensureFleet().then(askPeers);
  }

  function askPeers(): void {
    const ids = st.fleetRouters
      .filter((r) => !r.disabled && String(r.id) !== st.rid)
      .map((r) => String(r.id));
    if (!ids.length) {
      st.peers = [];
      applyData(); syncFleetBtn(); deps.render();
      return;
    }
    st.fleetBusy = true;
    syncFleetBtn();
    const seq = ++st.peersSeq;
    const forRouter = st.rid;
    fetch('/api/topology/peers?self=' + encodeURIComponent(st.rid || '') +
      '&routers=' + encodeURIComponent(ids.join(',')),
      { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        // THE ANSWER TO AN OLDER QUESTION IS NOT AN ANSWER. Switching router
        // with Fleet on leaves two reads in flight, and the slower one would
        // otherwise merge one router's peers into another router's map.
        if (seq !== st.peersSeq || forRouter !== st.rid) return;
        st.peers = (d && d.peers) || [];
        st.selfMacs = (d && d.self) || [];
      })
      .catch(() => {
        if (seq !== st.peersSeq || forRouter !== st.rid) return;
        st.peers = [];
        st.selfMacs = [];
      })
      .then(() => {
        if (seq !== st.peersSeq) return;
        st.fleetBusy = false;
        applyData(); syncFleetBtn(); deps.render();
      });
  }

  function syncFleetBtn(): void {
    const b = byId('topoFleetBtn');
    if (!b) return;
    b.classList.toggle('is-on', st.fleetOn);
    b.textContent = st.fleetBusy ? t('Fleet…')
      : (st.fleetOn && st.fleetStat.answered ? 'Fleet ' + st.fleetStat.answered : t('Fleet'));
  }

  // ── the operator's own cabling ────────────────────────────────────────────
  //
  // Stored per router through `/api/router-doc`, kind `topology-links`, and
  // applied by the COLLECTOR rather than here: a pin changes which device hangs
  // off which, and the layout, the edges and the client attribution all read
  // that. Drawing it browser-side would mean re-deriving three things this page
  // is handed.
  //
  // See internal/sitedoc.TopologyLinks for the shape, and `resolveParents` in
  // internal/collect/topology.go for what a pin overrides.

  function loadPins(): void {
    if (!st.rid || st.pinsLoadedFor === st.rid) return;
    // MARKED BEFORE THE REQUEST so a second call does not race it, and cleared
    // again on failure so it is retried rather than leaving the PREVIOUS
    // router's pins in place for the rest of the session.
    const want = st.rid;
    st.pinsLoadedFor = st.rid;
    fetch('/api/router-doc?kind=topology-links&routerId=' + encodeURIComponent(st.rid),
      { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('unreadable'))))
      .then((d) => {
        const doc = d && d.doc;
        st.pins = (doc && doc.parents) || {};
        st.pinsEnabled = !doc || doc.enabled !== false;
        st.pinsLoaded = true;
        syncPinsBtn();
        deps.renderPanel();
      })
      .catch(() => {
        if (st.pinsLoadedFor === want) st.pinsLoadedFor = '';
        st.pins = {};
        st.pinsLoaded = false;
        syncPinsBtn();
      });
  }

  function savePins(): void {
    if (!st.rid) return;
    // NOT OVER A READ THAT FAILED. An empty `pins` after an unreadable document
    // is indistinguishable from a router with none, and this would replace the
    // operator's cabling with nothing.
    if (!st.pinsLoaded) {
      st.pinsLoadedFor = '';
      loadPins();
      return;
    }
    fetch('/api/router-doc', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        routerId: st.rid, kind: 'topology-links',
        doc: { enabled: st.pinsEnabled, parents: st.pins },
      }),
    })
      // A REFUSAL IS NOT A SAVE. Without the `r.ok` test a 403 from a read-only
      // grant left the new pin in this browser's map and on the Pins counter,
      // while the store held nothing — so the panel's choice silently sprang
      // back on the next tick and the button stayed toggled.
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('refused'))))
      .then((d) => {
        // The server's copy wins: `sitedoc.CleanTopologyLinks` drops a pin that
        // names a loop or an empty half, and this browser must not go on drawing
        // one the store does not hold.
        const doc = d && d.doc;
        if (doc) {
          st.pins = doc.parents || {};
          st.pinsEnabled = doc.enabled !== false;
          st.pinsLoaded = true;
        }
        syncPinsBtn();
        deps.renderPanel();
      })
      .catch(() => {
        // BACK TO WHAT THE STORE HOLDS, rather than leaving this browser drawing
        // a pin nobody else has. The next load re-reads it.
        st.pinsLoadedFor = '';
        loadPins();
      });
  }

  function syncPinsBtn(): void {
    const b = byId('topoPinsBtn');
    if (!b) return;
    b.classList.toggle('is-on', st.pinsEnabled);
    const n = Object.keys(st.pins).length;
    b.textContent = n ? 'Pins ' + n : t('Pins');
  }

  return { applyData, loadPeers, syncFleetBtn, loadPins, savePins, syncPinsBtn };
}
