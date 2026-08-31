'use strict';
/**
 * A DOM shim THAT MODELS IDENTITY, for renderers that build nodes.
 *
 * ---- WHY THIS IS NOT `dom-shim.js` -----------------------------------------
 *
 * `dom-shim` stores markup. Its `createElement` and `appendChild` exist so that
 * a node-building renderer RUNS TO COMPLETION rather than throwing halfway and
 * skipping everything after it, and its header says the rest plainly: they do
 * not model node identity, so "anything built that way must NOT be COMPARED --
 * only allowed to happen."
 *
 * That is the right rule, and it is why this file exists rather than a looser
 * `dom-shim`. A renderer that KEEPS its rows and syncs them -- the shape the
 * live app moved to for the country list on 2026-08-25, and the shape the Sankey
 * has always had -- can only be compared by something that records what was
 * actually built: which node, in which order, with which attributes, and whether
 * a second render REUSED it or replaced it.
 *
 * ---- WHAT IT RECORDS -------------------------------------------------------
 *
 *   tag, attributes IN INSERTION ORDER, className, dataset, style, text,
 *   children, and a per-node serial.
 *
 * The SERIAL is the point. Two renders that produce identical markup are not the
 * same thing if the second threw the first's nodes away: the first keeps hover,
 * focus and listeners, the second does not. That difference is invisible to
 * every markup comparison in this repo, and it is exactly what ToDo #18 was
 * about.
 *
 * Attributes are kept in insertion order rather than sorted. Two sides that set
 * the same attributes in a different order draw the same picture, but the order
 * is the cheapest signal that one of them was rewritten.
 */

/**
 * A small parser for the markup a SKELETON is written in.
 *
 * Deliberately narrow: open tag with double-quoted attributes, text, close tag.
 * Self-closing and void elements are handled; comments, CDATA and unquoted
 * attributes are not, and a skeleton needing them should be built with
 * `createElement` instead of hidden behind a guess.
 */
function parseHTML(html, mk) {
  const out = [];
  const stack = [];
  const push = (node) => {
    if (stack.length) stack[stack.length - 1].appendChild(node);
    else out.push(node);
  };
  const re = /<\/?([a-zA-Z][\w-]*)((?:\s+[\w-]+(?:="[^"]*")?)*)\s*(\/?)>|([^<]+)/g;
  const VOID = new Set(['br', 'img', 'input', 'hr', 'meta', 'link']);
  let m;
  while ((m = re.exec(html))) {
    const [tag, name, attrs, selfClose, text] = m;
    if (text !== undefined) {
      const t = text.trim();
      if (t && stack.length) {
        const host = stack[stack.length - 1];
        host._text = (host._text || '') + t;
      }
      continue;
    }
    if (tag.startsWith('</')) { stack.pop(); continue; }
    const node = mk(name.toLowerCase());
    for (const a of attrs.matchAll(/([\w-]+)(?:="([^"]*)")?/g)) {
      if (!a[1]) continue;
      const key = a[1];
      const val = a[2] === undefined ? '' : a[2];
      if (key === 'class') node.className = val;
      else if (key === 'style') {
        for (const decl of val.split(';')) {
          const at = decl.indexOf(':');
          if (at < 0) continue;
          const prop = decl.slice(0, at).trim().replace(/-([a-z])/g, (_, c) => c.toUpperCase());
          node.style[prop] = decl.slice(at + 1).trim();
        }
      } else if (key.startsWith('data-')) {
        node.dataset[key.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = val;
        node.setAttribute(key, val);
      } else node.setAttribute(key, val);
    }
    push(node);
    if (!selfClose && !VOID.has(name.toLowerCase())) stack.push(node);
  }
  return out;
}

/** Build a document whose elements remember what was done to them. */
function makeTree() {
  let serial = 0;
  const created = [];

  /**
   * A compound selector: an optional TAG followed by any number of `.class` and
   * `[attr]` parts, all of which must hold. A space-separated selector is
   * matched on its LAST part only -- descendant structure is not modelled, and a
   * gate needing it should say so rather than have this quietly guess.
   *
   * The parts genuinely combine, because the page genuinely combines them:
   * `renderIfaceList` reads `tr[data-iface]` and the tile grid reads
   * `.iface-tile[data-iface]`. An earlier version handled a tag with ONE
   * trailing part and read the whole of `.iface-tile[data-iface]` as a class
   * name -- it matched nothing, every frame looked like a first frame, and that
   * is exactly the reuse this shim exists to see.
   */
  function matcher(sel) {
    const parts = String(sel).trim().split(/\s+/);
    const last = parts[parts.length - 1];
    const m = /^([a-zA-Z][\w-]*)?((?:[.#[][^.[]*\]?)*)$/.exec(last);
    if (!m) return () => false;
    const tag = m[1] || null;
    const tests = [];
    for (const tok of (m[2] || '').match(/[.#][\w-]+|\[[^\]]*\]/g) || []) {
      if (tok.startsWith('.')) {
        const cls = tok.slice(1);
        tests.push((el) => el.classList.contains(cls));
      } else if (tok.startsWith('[')) {
        const attr = tok.slice(1, -1).split('=')[0];
        const camel = attr.replace(/^data-/, '').replace(/-([a-z])/g, (_, c) => c.toUpperCase());
        tests.push((el) => el.getAttribute(attr) !== null || el.dataset[camel] !== undefined);
      }
    }
    if (!tag && !tests.length) return () => false;
    return (el) => {
      if (tag && el.tag !== tag) return false;
      return tests.every((t) => t(el));
    };
  }

  const mk = (tag) => {
    const n = {
      tag,
      serial: ++serial,
      attrs: [],
      kids: [],
      style: {},
      dataset: {},
      _text: '',
      _html: '',
      parentNode: null,

      setAttribute(k, v) { n.attrs.push([k, String(v)]); },
      getAttribute(k) {
        for (let i = n.attrs.length - 1; i >= 0; i--) if (n.attrs[i][0] === k) return n.attrs[i][1];
        return null;
      },
      removeAttribute(k) { n.attrs = n.attrs.filter(([a]) => a !== k); },

      appendChild(c) {
        if (c.parentNode) c.parentNode.removeChild(c);
        c.parentNode = n;
        n.kids.push(c);
        return c;
      },
      // MOVES rather than clones, which is the behaviour a reconcile loop
      // depends on: `insertBefore` on a node already in the list is how a row
      // keeps its identity through a reorder.
      insertBefore(c, ref) {
        if (c.parentNode) c.parentNode.removeChild(c);
        c.parentNode = n;
        const at = ref ? n.kids.indexOf(ref) : n.kids.length;
        n.kids.splice(at < 0 ? n.kids.length : at, 0, c);
        return c;
      },
      // `remove()` detaches THIS node. `renderIfaceList` drops a vanished
      // interface with it, and without it the row stays in the table forever.
      remove() { if (n.parentNode) n.parentNode.removeChild(n); },
      replaceChild(fresh, old) {
        const at = n.kids.indexOf(old);
        if (at < 0) return old;
        if (fresh.parentNode) fresh.parentNode.removeChild(fresh);
        n.kids[at] = fresh;
        fresh.parentNode = n;
        old.parentNode = null;
        return old;
      },
      // Only the two positions the pages actually use. An unsupported one
      // THROWS rather than being ignored: a silently-dropped insert would look
      // like a renderer that chose not to draw something.
      insertAdjacentHTML(where, html) {
        const built = parseHTML(String(html), mk);
        if (where === 'afterbegin') {
          for (let i = built.length - 1; i >= 0; i--) n.insertBefore(built[i], n.kids[0] || null);
        } else if (where === 'beforeend') {
          for (const c of built) n.appendChild(c);
        } else if (where === 'afterend') {
          if (!n.parentNode) return;
          let ref = n.nextSibling;
          for (const c of built) n.parentNode.insertBefore(c, ref);
        } else {
          throw new Error('tree-shim: insertAdjacentHTML(' + where + ') is not modelled');
        }
      },
      removeChild(c) {
        const at = n.kids.indexOf(c);
        if (at >= 0) n.kids.splice(at, 1);
        c.parentNode = null;
        return c;
      },
      contains(c) {
        for (let p = c; p; p = p.parentNode) if (p === n) return true;
        return false;
      },

      get firstChild() { return n.kids[0] || null; },
      get nextSibling() {
        if (!n.parentNode) return null;
        const at = n.parentNode.kids.indexOf(n);
        return n.parentNode.kids[at + 1] || null;
      },
      get children() { return n.kids.slice(); },
      get firstElementChild() { return n.kids[0] || null; },

      get textContent() { return n._text; },
      set textContent(v) { n._text = String(v); n.kids.length = 0; },

      // ---- innerHTML DESTROYS THE CHILDREN, THEN PARSES THE NEW ONES -------
      //
      // Destroying is the point: a renderer that assigns innerHTML has thrown
      // its rows away, and the serials of whatever it builds next say so.
      //
      // PARSING is what makes a build-and-sync renderer reachable at all. Both
      // the live country row and the port's build their SKELETON with one
      // innerHTML assignment and then write into it with `querySelector`. A shim
      // that only stored the string answered every one of those with null, and
      // the sync threw on the first cell — which reads like the page being
      // broken and is the shim being thin.
      //
      // It parses the shape these skeletons are: nested tags, double-quoted
      // attributes, text. Not a browser — `<` inside an attribute value or a
      // comment would defeat it — and a gate that needs more than that should
      // say so rather than have this quietly guess.
      get innerHTML() { return n._html; },
      set innerHTML(v) {
        n._html = String(v);
        for (const k of n.kids) k.parentNode = null;
        n.kids.length = 0;
        for (const child of parseHTML(String(v), mk)) n.appendChild(child);
      },

      classList: {
        _s: new Set(),
        add(c) { this._s.add(c); },
        remove(c) { this._s.delete(c); },
        contains(c) { return this._s.has(c); },
        toggle(c, on) {
          if (on === undefined) { this._s.has(c) ? this._s.delete(c) : this._s.add(c); }
          else if (on) this._s.add(c); else this._s.delete(c);
        },
      },

      // Element-scoped lookup over the tree this node actually holds, by class,
      // tag or `[data-x]` -- enough for a sync function reaching for the cells
      // it is about to write.
      querySelector(sel) { return n.querySelectorAll(sel)[0] || null; },
      querySelectorAll(sel) {
        const out = [];
        const want = matcher(sel);
        const walk = (p) => { for (const k of p.kids) { if (want(k)) out.push(k); walk(k); } };
        walk(n);
        return out;
      },
      closest(sel) {
        const want = matcher(sel);
        for (let p = n; p; p = p.parentNode) if (want(p)) return p;
        return null;
      },
      focus() {},
      addEventListener(ev, fn) { (n._on = n._on || {})[ev] = (n._on[ev] || []).concat(fn); },
      removeEventListener(ev, fn) {
        if (n._on && n._on[ev]) n._on[ev] = n._on[ev].filter((f) => f !== fn);
      },
      fire(ev, extra) {
        for (const fn of ((n._on || {})[ev] || []).slice()) {
          fn(Object.assign({ target: n, preventDefault() {}, stopPropagation() {} }, extra));
        }
      },
    };
    Object.defineProperty(n, 'className', {
      get: () => [...n.classList._s].join(' '),
      set: (v) => { n.classList._s = new Set(String(v).split(/\s+/).filter(Boolean)); },
      enumerable: true,
    });
    created.push(n);
    return n;
  };

  return { mk, created };
}

/**
 * Serialise a node for comparison.
 *
 * `serial` is included by default. Pass `{ serials: false }` for a comparison
 * that is only about the PICTURE -- two implementations whose rows are equally
 * stable but numbered differently should not fail on the numbering, and a gate
 * comparing two sides at one instant usually wants that.
 */
function serialise(n, opts) {
  const o = opts || {};
  const out = {
    tag: n.tag,
    cls: [...n.classList._s].sort().join(' ') || undefined,
    attrs: n.attrs.length ? n.attrs : undefined,
    data: Object.keys(n.dataset).length ? n.dataset : undefined,
    style: Object.keys(n.style).length ? n.style : undefined,
    text: n._text || undefined,
    html: n._html || undefined,
    kids: n.kids.length ? n.kids.map((k) => serialise(k, o)) : undefined,
  };
  if (o.serials !== false) out.serial = n.serial;
  return out;
}

module.exports = { makeTree, serialise };
