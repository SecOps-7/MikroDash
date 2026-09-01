// THE DOCUMENT SHIM THE WEB TESTS SHARE.
//
// Moved from `tools/lib/dom-shim.js` when the port-parity harness was retired.
// Its body is VERBATIM: every lesson in it (IDL type coercion, ids realised from
// innerHTML, delegated listener recording) is about simulating a browser
// correctly, not about the implementation this app replaced, so none of it went
// stale when that did.
//
// ── WHY IT IS NOT STRICTLY TYPED ────────────────────────────────────────────
//
// It fabricates node objects that stand in for the DOM. Typing them against
// lib.dom would either be a lie or a very large exercise in describing a
// deliberate approximation, so `web/test/tsconfig.json` relaxes the project's
// strictness for this directory only. The code under test keeps the full rules.

import assert from 'node:assert';
/**
 * THE DOCUMENT SHIM THE PAGE GATES SHARE.
 *
 * Deliberately NOT a DOM library. A real one silently normalises markup —
 * reordering attributes, closing tags, lower-casing — and a comparison of two
 * NORMALISED strings can pass while the raw markup differs. These nodes store
 * what was assigned.
 *
 * ── EVERY RULE BELOW WAS A BUG FIRST ────────────────────────────────────────
 *
 * Twenty-eight gates each grew their own version of this, each a different
 * subset, and the same three mistakes kept reappearing. They are collected here
 * so the next gate inherits the lessons instead of rediscovering them:
 *
 * 1. THE IDL TYPES ARE ENFORCED. `value` and `title` are DOMString; `checked`
 *    and `disabled` are boolean; so are `textContent` and `innerHTML` (string).
 *    A live page assigning `el('bkKeepCount').value = 10` — a NUMBER — is stored
 *    by a browser as "10". A shim that kept the number reported ALL 42 cases of
 *    the Backups gate as differing against a port that assigns a string. That is
 *    a false failure of the most damaging kind: it looks like a real find, and
 *    acting on it would have "fixed" correct code.
 *
 * 2. IDS INSIDE ASSIGNED innerHTML BECOME REAL NODES. Pages write buttons as a
 *    STRING and then look them up by id to set `disabled`/`textContent`/`title`.
 *    Without this the lookup returns null on BOTH sides, the code no-ops on BOTH
 *    sides, and the gate passes having compared nothing. This is the FALSE PASS
 *    direction and it is the dangerous one.
 *
 *    Two modules in this port write-then-look-up: `backups` and `queues`.
 *
 * 3. DOCUMENT LISTENERS ARE RECORDED so a gate can drive the behaviour a page
 *    actually implements. Selection, tab switching and bulk actions all live on
 *    delegated `document` listeners; a no-op `addEventListener` leaves the gate
 *    seeding one side's internal state by hand, which compares a harness against
 *    an implementation rather than two implementations against each other.
 *
 * 8. `createElement` / `appendChild` exist so a NODE-BUILDING renderer runs to
 *    completion instead of throwing halfway and skipping everything after it in
 *    the same handler. They do NOT model node identity, so anything built that
 *    way must not be COMPARED — only allowed to happen.
 *
 * 7. `children` / `firstElementChild` / `removeChild` work over the stored
 *    markup by a tag-balance scan, so a streaming page's cap
 *    (`while (el.children.length > MAX) el.removeChild(el.firstElementChild)`)
 *    can be driven. It is a scan, not a DOM: top-level elements only, no
 *    comments or unclosed tags.
 *
 * 6. `insertAdjacentHTML` APPENDS, in terms of innerHTML, so a streaming page
 *    that adds one line rather than rewriting the list is drivable. Only the two
 *    positions that appear in this codebase are implemented; an unimplemented
 *    one THROWS rather than silently doing nothing, because a silent no-op here
 *    is a gate comparing two pages that both rendered less than they should.
 *
 * 5. ELEMENT-LEVEL LISTENERS ARE RECORDED, and `node.fire(ev)` dispatches them.
 *    A search box, a select and a button are wired to themselves rather than to
 *    `document`; without this a gate can only reach filter state by poking at a
 *    closure, which is not a comparison of two implementations.
 *
 * 4. A BARE TAG SELECTOR IS ANSWERED, and the cells it returns record their
 *    click listeners. Sort headers are written as a string and then walked to
 *    attach handlers; a shim that returns [] leaves them unwired, so the sort
 *    DIRECTION is never exercised and a mutation ignoring it survives. That was
 *    measured on the DNS gate before this existed.
 *
 * WHAT IT STILL CANNOT SEE, and no gate built on it may claim otherwise: layout,
 * focus, computed style, real event dispatch and bubbling, anything that is not
 * one of the properties listed above.
 */

// value/title are DOMString, checked/disabled are boolean — see rule 1.
const IDL = [
  ['value', String], ['title', String],
  ['checked', Boolean], ['disabled', Boolean], ['indeterminate', Boolean],
];

/**
 * @param {string[]} ids            ids present in the page's markup
 * @param {object}   [opts]
 * @param {string[]} [opts.pickSelectors]  selectors querySelectorAll must answer,
 *                                         matched against assigned innerHTML
 * 11. A <select>'s value follows its options — see the innerHTML setter.
 */
function makeDoc(ids, opts) {
  // ── THE CALLER'S OPTIONS ARE NOT MUTATED ────────────────────────────────
  //
  // The element-query tables below used to be written back into the object the
  // caller passed, so a gate handing the SAME options to two runs gave the
  // second one node objects where the first got a list of attribute values. The
  // second run's buttons then answered `getAttribute` with nothing, and the two
  // sides differed on a case where neither had touched a tab.
  //
  // Found on 2026-08-25 in `bridges-page-check`, which builds its live and port
  // documents from one shared constant — the natural way to write it, and the
  // way that guarantees both sides get the same shim.
  const o = Object.assign({}, opts || {},
    { elementQuery: Object.assign({}, (opts || {}).elementQuery) });
  const nodes = {};
  const listeners = {};
  const unknown = new Set();
  const parents = {};

  /** One shared node per parent NAME: two children of one element get one parent. */
  const parentFor = (name) => {
    if (!parents[name]) {
      parents[name] = {
        name, style: {}, dataset: {}, attributes: {},
        setAttribute(k, v) { this.attributes[k] = String(v); },
        classList: {
          _s: new Set(),
          add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); },
          contains(c) { return this._s.has(c); },
          toggle(c, on) { if (on === undefined) { this._s.has(c) ? this._s.delete(c) : this._s.add(c); } else if (on) this._s.add(c); else this._s.delete(c); },
        },
      };
    }
    return parents[name];
  };

  const mk = (id) => {
    const store = { innerHTML: '', textContent: '' };
    let cells = {};
    const n = { id, style: {}, dataset: {}, outerHTML: '' };

    Object.defineProperty(n, 'innerHTML', {
      get: () => store.innerHTML,
      set: (v) => {
        store.innerHTML = String(v);
        cells = {};   // a re-render replaces the cells, listeners included
        // Rule 2.
        for (const m of store.innerHTML.matchAll(/\bid="([A-Za-z0-9_-]+)"/g)) {
          if (!nodes[m[1]]) nodes[m[1]] = mk(m[1]);
        }
        // Rule 11: A <select>'s VALUE FOLLOWS ITS OPTIONS. Replacing the
        // options of a real select resets `value` to the first option, or to ''
        // when there are none — the old value does not survive unless the code
        // puts it back, which is exactly what "keep the current choice if it
        // still exists" means.
        //
        // Without this the shim kept the old value through a re-render, so a
        // gate could not tell "preserved deliberately" from "never lost", and a
        // vanished interface appeared to survive. Found by a believability
        // assert on the Reports selector, which encodes the browser rule.
        const opts = [...store.innerHTML.matchAll(/<option[^>]*\svalue="([^"]*)"/g)].map((m) => m[1]);
        if (opts.length || /<option\b/.test(store.innerHTML)) {
          store.value = opts.length ? opts[0] : '';
        }
      },
      enumerable: true,
    });
    Object.defineProperty(n, 'textContent', {
      get: () => store.textContent,
      set: (v) => { store.textContent = String(v); },
      enumerable: true,
    });
    for (const [k, coerce] of IDL) {
      store[k] = coerce('');
      Object.defineProperty(n, k, {
        get: () => store[k], set: (v) => { store[k] = coerce(v); }, enumerable: true,
      });
    }

    // ── ELEMENT-LEVEL LISTENERS ARE RECORDED TOO ──────────────────────────
    //
    // Most controls are wired to the element, not to `document`: a search box's
    // `input`, a select's `change`, a button's `click`. A no-op here leaves a
    // gate unable to drive any of them, so filter and sort state can only be
    // reached by reaching into a closure — which compares a harness against an
    // implementation rather than two implementations against each other.
    n._listeners = {};
    n.addEventListener = (ev, fn) => { (n._listeners[ev] = n._listeners[ev] || []).push(fn); };
    // `this` IS THE ELEMENT, as a real addEventListener binds it. Handlers
    // written `function () { this.value }` are ordinary — the Interfaces type
    // filter is one — and calling them unbound throws on `this.classList` in
    // strict mode, which reads as a port defect rather than a harness gap.
    n.fire = (ev, extra) => {
      for (const fn of (n._listeners[ev] || []).slice()) {
        fn.call(n, Object.assign({ target: n }, extra));
      }
    };
    // Streaming pages APPEND rather than replace — a log line arrives and is
    // added, which is the whole point of not rewriting 2,000 lines per message.
    // Implemented in terms of innerHTML so the id registration and cell-cache
    // invalidation above apply to inserted markup as well.
    n.insertAdjacentHTML = (position, html) => {
      const cur = store.innerHTML;
      if (position === 'beforeend') n.innerHTML = cur + html;
      else if (position === 'afterbegin') n.innerHTML = html + cur;
      else throw new Error('the shim does not implement insertAdjacentHTML(' + position + ')');
    };
    // ── TOP-LEVEL CHILDREN, PARSED FROM THE MARKUP ────────────────────────
    //
    // A streaming page caps its list with real child-node calls:
    // `while (el.children.length > MAX) el.removeChild(el.firstElementChild)`.
    // Without these the cap cannot be driven, and "2,000 lines" is exactly the
    // kind of bound that is wrong by one and never noticed.
    //
    // This is a TAG-BALANCE SCAN over the stored string, not a DOM. It finds
    // top-level elements only, which is what these lists contain; it does not
    // handle comments, CDATA or unclosed tags, and it is not trying to.
    const topLevel = () => {
      const html = store.innerHTML;
      const out = [];
      {
        let i = 0;
        while (i < html.length) {
          const open = html.indexOf('<', i);
          if (open < 0) break;
          const om = /^<([a-zA-Z][\w-]*)\b[^>]*?(\/?)>/.exec(html.slice(open));
          if (!om) { i = open + 1; continue; }
          if (om[2] === '/') { out.push([open, open + om[0].length]); i = open + om[0].length; continue; }
          const name = om[1];
          let d = 1, j = open + om[0].length;
          const tagRe = new RegExp('<(/?)' + name + '\\b[^>]*?>', 'g');
          tagRe.lastIndex = j;
          let t;
          while (d > 0 && (t = tagRe.exec(html)) !== null) { d += t[1] ? -1 : 1; j = tagRe.lastIndex; }
          out.push([open, j]);
          i = j;
        }
      }
      return out.map(([a2, b2]) => html.slice(a2, b2));
    };
    Object.defineProperty(n, 'children', {
      get: () => topLevel().map((h) => ({ outerHTML: h })),
      enumerable: true,
    });
    Object.defineProperty(n, 'firstElementChild', {
      get: () => { const c = topLevel(); return c.length ? { outerHTML: c[0] } : null; },
      enumerable: true,
    });
    n.removeChild = (child) => {
      const at = store.innerHTML.indexOf(child.outerHTML);
      if (at < 0) throw new Error('removeChild: node is not a child of this element');
      n.innerHTML = store.innerHTML.slice(0, at) +
                    store.innerHTML.slice(at + child.outerHTML.length);
      return child;
    };
    // An element-level `querySelector` returns null: the page takes its
    // not-found branch, which is what a browser does for a selector that matches
    // nothing. Recorded, so a gate can see what it did not provide.
    // ── ENOUGH TO LET A NODE-BUILDING RENDERER FINISH ─────────────────────
    //
    // NOT node identity, and that distinction is the whole point. A renderer that
    // calls `createElement`/`appendChild` needs these to exist or it throws
    // halfway and everything AFTER it in the same handler never runs — which is
    // how the Interfaces type panel went uncompared. Appending records the
    // child's markup so the parent is not empty.
    //
    // What this does NOT give you is stable identity: a second render creates
    // fresh nodes, so a diffing renderer's "reuse the existing row" path cannot
    // be exercised. Any element built this way must NOT be compared by a gate —
    // it would be comparing the shim's approximation, not the page.
    n.appendChild = (child) => {
      n.innerHTML = store.innerHTML + (child && child.outerHTML !== undefined
        ? child.outerHTML : (child && child.innerHTML) || '');
      return child;
    };
    n.querySelector = () => null;
    n.getAttribute = () => null;
    // `setAttribute` records rather than throwing: `aria-selected` is set on
    // every tab switch, and a page that cannot set an attribute stops before it
    // finishes switching.
    n.attributes = {};
    n.setAttribute = (k, v) => { n.attributes[k] = String(v); };
    n.removeAttribute = (k) => { delete n.attributes[k]; };
    n.hasAttribute = () => false;
    n.closest = () => null;

    // ── PARENTS, WHEN THE EXTRACTED MARKUP DESCRIBES ONE ────────────────────
    //
    // A page that toggles a class on `table.parentElement` — the Firewall page
    // does, for `fw-noedit` — is unreachable through a shim whose nodes have no
    // parent, and the toggle then happens on NEITHER side and compares equal.
    // `firewall-table-check` recorded exactly that as a surviving mutation, with
    // the note that faking a parent would be inventing structure.
    //
    // It is not invention when the structure is written down: `web/src/ui`
    // holds the page markup this port serves, and `#firewallTable`'s parent is
    // a `<table>` there. `opts.parents` maps a child id to a NAME for its
    // parent, and the gate reads the result back through `doc.parents[name]`.
    // The name is the gate's own label — the parent has no id, which is why the
    // page cannot reach it by one either.
    if ((o.parents || {})[id]) n.parentElement = parentFor(o.parents[id]);
    n.classList = {
      _s: new Set(),
      add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); },
      contains(c) { return this._s.has(c); },
      toggle(c, on) { if (on === undefined) { this._s.has(c) ? this._s.delete(c) : this._s.add(c); } else if (on) this._s.add(c); else this._s.delete(c); },
    };

    // Elements carrying a data attribute, parsed out of the markup this node was
    // given. Only the selectors a gate declares are answered — an unrecognised
    // one returns [] rather than pretending, so a gate cannot quietly rely on
    // matching this shim does not do.
    n.querySelectorAll = (sel) => {
      // ── ELEMENT-SCOPED QUERY NODES ────────────────────────────────────────
      //
      // A well-written page scopes its lookups: `bar.querySelectorAll('.stab')`
      // rather than `document.querySelectorAll`. The live Routing page says so
      // explicitly — the switcher it was modelled on queries document-wide,
      // which is safe only while exactly one such strip exists. A shim that
      // answers only at document level leaves those pages unswitchable, and
      // everything behind their tabs uncompared.
      //
      // `opts.elementQuery` maps an element id to its own selector table, in the
      // same shape as `opts.query`.
      const scoped = (o.elementQuery || {})[id];
      if (scoped && scoped[sel]) return scoped[sel];
      // A BARE TAG SELECTOR, e.g. 'th'. `_renderSortHeader` writes the header
      // cells as a string and then walks them to attach click handlers, so
      // without this the sort header is written and never wired — and the sort
      // DIRECTION becomes uncomparable on every table that has one. The nodes
      // returned RECORD their listeners so a gate can click one.
      if (/^[a-z]+$/.test(sel)) {
        // CACHED PER ASSIGNMENT. The cells are created during a render, which is
        // when their click listeners are attached; a later lookup must return
        // THOSE objects or the listeners are unreachable and the click cannot be
        // driven. The cache is dropped whenever innerHTML is reassigned, because
        // a re-render replaces the cells for real.
        cells[sel] = cells[sel] || (() => {
          const out = [];
          const re = new RegExp('<' + sel + '\\b[^>]*>', 'g');
          for (const m of store.innerHTML.matchAll(re)) {
            const cell = mk('');
            cell.outerHTML = m[0];
            cell._clicks = [];
            cell.addEventListener = (ev, fn) => { if (ev === 'click') cell._clicks.push(fn); };
            cell.click = () => { for (const fn of cell._clicks.slice()) fn({ target: cell }); };
            out.push(cell);
          }
          return out;
        })();
        return cells[sel];
      }
      if (!(o.pickSelectors || []).includes(sel)) return [];
      const attr = (sel.match(/\[([a-z-]+)\]/) || [])[1];
      if (!attr) return [];
      const onlyEnabled = sel.includes(':not([disabled])');
      const out = [];
      const re = new RegExp('<[a-z]+\\b[^>]*\\b' + attr + '="([^"]*)"[^>]*>', 'g');
      for (const m of store.innerHTML.matchAll(re)) {
        const tag = m[0];
        const disabled = / disabled[ >]/.test(tag);
        if (onlyEnabled && disabled) continue;
        out.push({
          checked: / checked[ >]/.test(tag), disabled, value: '',
          getAttribute: (a) => (a === attr ? m[1] : null),
          hasAttribute: (a) => a === attr,
          closest: () => null,
          classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
        });
      }
      return out;
    };
    return n;
  };

  for (const id of ids) nodes[id] = mk(id);

  // ── DOCUMENT-LEVEL QUERY NODES, DECLARED BY THE GATE ──────────────────────
  //
  // A page that finds its controls with `document.querySelectorAll('[data-x]')`
  // — tab bars usually do — gets an empty list from a shim that answers []. The
  // page then wires nothing, and a gate cannot reach any state behind those
  // controls: the Router Users tabs were unswitchable, so two mutations in the
  // hidden-tab render survived undetected.
  //
  // `opts.query` maps a selector to the ATTRIBUTE VALUES the gate wants to
  // exist. Each becomes a node carrying that attribute, with listeners recorded
  // and `click()` available. Only declared selectors are answered; anything else
  // is still [], and recorded as unknown.
  const makeQueryNodes = (table) => {
    const out = {};
    for (const [sel, values] of Object.entries(table || {})) {
      const attr = (o.queryAttr && o.queryAttr[sel])
        || (sel.match(/\[([a-z-]+)\]/) || [])[1];
      out[sel] = (values || []).map((v) => {
        const spec = typeof v === 'object' && v !== null ? v : { value: v };
        const node = mk(spec.id || '');
        if (spec.id) nodes[spec.id] = node;
        node.getAttribute = (a2) => (a2 === attr ? (spec.value ?? null) : null);
        // ── `dataset` FOLLOWS THE ATTRIBUTE, because pages read it both ways ──
        //
        // `getAttribute('data-rtab')` and `.dataset.rtab` are the same thing to
        // a browser and were not here: `dataset` started empty, so a handler
        // written the second way saw `undefined` and looked up `#rtab-undefined`.
        // The Reports tab strip is written that way on BOTH sides, so the panel
        // switch could not be driven at all — and it failed as "no panel is
        // shown", which reads like a port bug rather than a shim gap.
        if (attr && attr.startsWith('data-') && spec.value !== undefined) {
          const camel = attr.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
          node.dataset[camel] = spec.value;
        }
        node.hasAttribute = (a2) => a2 === attr && spec.value !== undefined;
        node.click = () => node.fire('click');
        // A NO-OP `focus`, and it is honest rather than lazy. The ARIA tablist
        // pattern ends each arrow move with `next.focus()`; without this the
        // handler throws halfway through and the tab never changes, so the gate
        // would report a difference caused by the shim. Focus itself is not
        // compared — nothing here can observe it — and that is already in the
        // header's list of what this shim does not model.
        node.focus = () => {};
        return node;
      });
    }
    return out;
  };
  for (const key of Object.keys(o.elementQuery || {})) {
    // Alias entries are STRINGS and are held back: `makeQueryNodes` expects a
    // list of values per selector and throws on one.
    const raw = o.elementQuery[key];
    const aliases = {}, real = {};
    for (const [sel, v] of Object.entries(raw)) {
      if (typeof v === 'string') aliases[sel] = v; else real[sel] = v;
    }
    const table = makeQueryNodes(real);
    // ── ONE SELECTOR MAY ALIAS ANOTHER, AND MUST SHARE ITS NODES ──────────
    //
    // A page often reaches the same controls two ways: the Routing tab strip is
    // read as `.stab` by the click path and as `[data-rttab]` by the arrow-key
    // path. Declaring both with equal-looking arrays builds TWO SETS of node
    // objects, so one path marks a button active and the other reports it is
    // not — a gate would then compare state the page never had. Writing the
    // other selector's NAME as the value aliases it instead.
    for (const [sel, v] of Object.entries(aliases)) {
      assert.ok(table[v], 'elementQuery alias ' + sel + ' -> ' + v + ': no such selector');
      table[sel] = table[v];
    }
    o.elementQuery[key] = table;
  }

  const queryNodes = {};
  for (const [sel, values] of Object.entries(o.query || {})) {
    // The attribute is taken from the selector when it names one
    // (`[data-x]`), or declared explicitly for a CLASS selector — live pages
    // often select `#bar .tab` and then read `data-something` off the match.
    const attr = (o.queryAttr && o.queryAttr[sel])
      || (sel.match(/\[([a-z-]+)\]/) || [])[1];
    // A value is either the ATTRIBUTE VALUE the node should carry, or an object
    // naming an `id` — panes are matched by id, buttons by attribute, and a
    // shim that only did one of those left the pane switch uncompared.
    queryNodes[sel] = (values || []).map((v) => {
      const spec = typeof v === 'object' && v !== null ? v : { value: v };
      const node = mk(spec.id || '');
      if (spec.id) nodes[spec.id] = node;
      node.getAttribute = (a2) => (a2 === attr ? (spec.value ?? null) : null);
      node.hasAttribute = (a2) => a2 === attr && spec.value !== undefined;
      node.click = () => node.fire('click');
      return node;
    });
  }


  return {
    nodes, listeners, unknown, queryNodes, parents,
    // Rule 3.
    addEventListener(ev, fn) { (listeners[ev] = listeners[ev] || []).push(fn); },
    // An id neither side declared is RECORDED rather than silently null: a
    // renderer asking for an element the gate forgot is a hole in the gate, not
    // a property of the page.
    getElementById(id) {
      if (nodes[id]) return nodes[id];
      unknown.add(id);
      return null;
    },
    querySelectorAll: (sel) => {
      if (queryNodes[sel]) return queryNodes[sel];
      unknown.add(sel);
      return [];
    },
    // A document-level `querySelector` returns null rather than throwing: a page
    // that looks for an element this gate did not declare should take its
    // not-found branch, which is what a real browser does for an id the page
    // does not have. Recorded like any other unknown lookup.
    querySelector: (sel) => { unknown.add(sel); return null; },
    createElement: () => mk(''),
    // SVG elements are created through a namespaced call. Same node either way
    // here — the shim does not model namespaces, and a gate that COMPARED SVG
    // built this way would be comparing the shim's approximation (see rule 8).
    createElementNS: () => mk(''),
    dispatch(ev, target) { for (const fn of listeners[ev] || []) fn({ target }); },
    // A page that ANNOUNCES something — `document.dispatchEvent(new
    // CustomEvent('mikrodash:...'))` — must be able to, or it throws partway
    // through a render and everything after it is lost. The event is delivered
    // to recorded listeners, so a gate driving two pages at once sees the
    // handoff rather than a crash.
    dispatchEvent(e) {
      const type = e && e.type;
      for (const fn of (listeners[type] || []).slice()) fn(e);
      return true;
    },
  };
}

/** Run `fn` with `globalThis.document` pointed at `doc`, then restore it. */
function withDocument(doc, fn) {
  const prev = globalThis.document;
  globalThis.document = doc;
  try { return fn(); } finally {
    if (prev === undefined) delete globalThis.document; else globalThis.document = prev;
  }
}

export { makeDoc, withDocument };
