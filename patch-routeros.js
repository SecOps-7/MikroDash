/**
 * patch-routeros.js — MikroDash v0.3.2
 *
 * Patches node-routeros (archived 2024) for RouterOS 7.18+ compatibility.
 * Run once after `npm install` (see Dockerfile).
 *
 * Patches applied:
 *  1. Channel.js   — handle !empty reply (ROS 7.18+: empty result set)
 *  2. Receiver.js  — handle UNREGISTEREDTAG gracefully (trailing packets after
 *                    stream/command completes) instead of crashing the process
 *  3. Receiver.js  — decode API strings as UTF-8 instead of win1252 so that
 *                    non-Latin characters (Cyrillic, Greek, etc.) display correctly
 */

'use strict';
const fs   = require('fs');
const path = require('path');

const BASE = path.join(__dirname, 'node_modules', 'node-routeros', 'dist');

function patch(filePath, description, replacements) {
  if (!fs.existsSync(filePath)) {
    console.warn('[patch] File not found, skipping:', filePath);
    return false;
  }

  let src = fs.readFileSync(filePath, 'utf8');

  if (src.includes('MIKRODASH_PATCHED_' + description)) {
    console.log('[patch]', description, '— already applied, skipping');
    return true;
  }

  let applied = 0;
  for (const { find, replace } of replacements) {
    if (src.includes(find)) {
      src = src.replace(find, `// MIKRODASH_PATCHED_${description}\n        ` + replace);
      applied++;
    }
  }

  if (applied === 0) {
    console.warn('[patch]', description, '— target string not found (library version mismatch?)');
    console.warn('[patch] App will start but may crash on edge cases. File:', filePath);
    return false;
  }

  fs.writeFileSync(filePath, src, 'utf8');
  console.log('[patch]', description, '— applied successfully');
  return true;
}

// ── Patch 1: Channel.js — !empty reply ──────────────────────────────────────
// RouterOS 7.18+ sends !empty when a command returns zero results.
// The library throws RosException('UNKNOWNREPLY') on any unknown reply type.
// Fix: treat !empty as an empty done (resolve with []).
patch(
  path.join(BASE, 'Channel.js'),
  'EMPTY_REPLY',
  [
    {
      find: `throw new RosException_1.RosException('UNKNOWNREPLY', { reply: reply });`,
      replace: `if (reply === '!empty') { this.emit('done', []); return; }
        throw new RosException_1.RosException('UNKNOWNREPLY', { reply: reply });`,
    },
    {
      // alternate double-quote form in some builds
      find: `throw new RosException_1.RosException("UNKNOWNREPLY", { reply: reply });`,
      replace: `if (reply === '!empty') { this.emit('done', []); return; }
        throw new RosException_1.RosException("UNKNOWNREPLY", { reply: reply });`,
    },
  ]
);

// ── Patch 2: Receiver.js — UNREGISTEREDTAG ──────────────────────────────────
// When RouterOS sends a packet for a tag that the library has already cleaned
// up (e.g. a trailing packet after !done, or a delayed response after a stream
// is stopped), the library throws RosException('UNREGISTEREDTAG') synchronously
// inside a socket data event — completely uncatchable by user code.
// Fix: log a debug warning and discard the packet instead of crashing.
patch(
  path.join(BASE, 'connector', 'Receiver.js'),
  'UNREGISTEREDTAG',
  [
    {
      find: `throw new RosException_1.RosException('UNREGISTEREDTAG');`,
      replace: `// Discard packets for tags we no longer track (e.g. trailing !done after stream stop)
        if (process.env.ROS_DEBUG === 'true') {
            console.warn('[routeros] discarded packet for unregistered tag:', tag);
        }
        return;`,
    },
    {
      find: `throw new RosException_1.RosException("UNREGISTEREDTAG");`,
      replace: `if (process.env.ROS_DEBUG === 'true') {
            console.warn('[routeros] discarded packet for unregistered tag:', tag);
        }
        return;`,
    },
  ]
);

// ── Patch 3: Receiver.js — UTF-8 string decoding ────────────────────────────
// node-routeros hardcodes win1252 when it converts raw TCP bytes to JS strings.
// RouterOS sends UTF-8 strings (confirmed in 6.x and 7.x), so win1252 mangles
// any non-Latin characters (Cyrillic, Greek, etc.) into garbage sequences.
// Switching to utf8 fixes device names, DHCP comments, interface labels, etc.
patch(
  path.join(BASE, 'connector', 'Receiver.js'),
  'UTF8_ENCODING',
  [
    {
      find: `this.currentLine += iconv.decode(data, 'win1252');`,
      replace: `this.currentLine += iconv.decode(data, 'utf8');`,
    },
    {
      find: `this.currentLine += iconv.decode(data, "win1252");`,
      replace: `this.currentLine += iconv.decode(data, "utf8");`,
    },
    {
      // second decode call — handles the case where the buffer contains more
      // data than the current token length requires (sliced into tmpBuffer)
      find: `const tmpStr = iconv.decode(tmpBuffer, 'win1252');`,
      replace: `const tmpStr = iconv.decode(tmpBuffer, 'utf8');`,
    },
    {
      find: `const tmpStr = iconv.decode(tmpBuffer, "win1252");`,
      replace: `const tmpStr = iconv.decode(tmpBuffer, "utf8");`,
    },
  ]
);

console.log('[patch] Done.');
