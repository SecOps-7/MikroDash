'use strict';
/**
 * Undo and redo for resource writes — the part that can be reasoned about
 * without a socket.
 *
 * Every write the engine performs is recorded as a pair of operations: the one
 * that was done, and the one that reverses it. Undo runs the reverse, redo runs
 * the forward again. Nothing here talks to a router; index.js applies these.
 *
 * ── Positions are ANCHORS, never indexes ──────────────────────────────────
 *
 * For an ordered resource — the firewall — restoring a row means putting it
 * back where it was, and "where it was" is recorded as *the id of the row it
 * sat immediately before*, or null for the end of the table. An ordinal would
 * be wrong the moment anything else in the table moved, and the whole point of
 * undo is that time has passed since the action.
 *
 * That anchor can itself be deleted, and then the entry is unusable. It is
 * refused rather than approximated: putting a firewall rule back in roughly the
 * right place is worse than saying it cannot be done.
 *
 * ── What is deliberately not recorded ─────────────────────────────────────
 *
 * A secret. `rowValues()` never returns one, so a `before` cannot contain a
 * pre-shared key, and undoing an edit that changed one leaves the current key
 * alone rather than restoring a value this process never had.
 */

/** The verbs an entry can ask index.js to perform. */
const OPS = Object.freeze(['add', 'remove', 'set', 'move', 'enable', 'disable']);

const SEP = '\u0001';

/**
 * A short sentence for the button's tooltip: "undo delete of 192.0.2.0/24".
 *
 * The identity is a composite for some resources, so its separator is swapped
 * for something readable rather than shown raw.
 */
function label(resource, what, identity) {
  const name = String(identity || '').split(SEP).filter(Boolean).join(' ');
  const verb = { create: 'add', update: 'edit', delete: 'delete',
                 move: 'move', enable: 'enable', disable: 'disable' }[what] || what;
  return verb + ' of ' + (name || String(resource.label || '').toLowerCase());
}

/**
 * Record a completed write.
 *
 * `before` and `after` are resource-named values (what rowValues() produces),
 * not RouterOS rows. `anchorBefore` / `anchorAfter` are the id the row sat
 * before, and are only meaningful for an ordered resource.
 */
function buildEntry({ resource, what, id, identity, before, after, anchorBefore, anchorAfter }) {
  let forward, reverse;

  switch (what) {
    case 'create':
      forward = { op: 'add', values: after, anchor: anchorAfter };
      reverse = { op: 'remove', id };
      break;

    case 'delete':
      forward = { op: 'remove', id };
      // Re-adding gives the row a NEW id, so the anchor is how it finds its
      // place again. index.js writes the new id back into the other half.
      reverse = { op: 'add', values: before, anchor: anchorBefore };
      break;

    case 'update':
      forward = { op: 'set', id, values: after };
      reverse = { op: 'set', id, values: before };
      break;

    case 'move':
      forward = { op: 'move', id, anchor: anchorAfter };
      reverse = { op: 'move', id, anchor: anchorBefore };
      break;

    case 'enable':
      forward = { op: 'enable', id };
      reverse = { op: 'disable', id };
      break;

    case 'disable':
      forward = { op: 'disable', id };
      reverse = { op: 'enable', id };
      break;

    default:
      return null;                    // an action with no inverse is not recorded
  }

  return { resource: resource.key, what, identity: identity || '', forward, reverse,
           label: label(resource, what, identity) };
}

/**
 * Keep both halves pointing at the row that now exists.
 *
 * An `add` has no id until it runs, so applying one has to write the resulting
 * id back into the entry — otherwise the matching `remove` would have nothing
 * to address.
 */
function rebind(entry, id) {
  if (!entry) return;
  for (const op of [entry.forward, entry.reverse]) if (op && op.op !== 'add') op.id = id;
}

module.exports = { OPS, SEP, buildEntry, label, rebind };
