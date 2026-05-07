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
 *  4. Channel.js   — accumulate multi-block !done responses (wifi-qcom devices
 *                    send one !done per interface; without this patch only the
 *                    first interface's clients are returned)
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

// ── Patch 4: Channel.js — multi-block !done accumulation ────────────────────
// RouterOS wifi-qcom devices (hAP ax2, hAP AX³) send /interface/wifi/
// registration-table/print as SEPARATE response blocks per interface, each
// terminated by its own !done. The library resolves the write() Promise on
// the FIRST !done, so only one interface's clients are returned and all
// subsequent blocks are discarded as UNREGISTEREDTAG packets.
//
// Fix: instead of resolving immediately on !done, start a 20 ms debounce
// timer. If more !re/!done blocks arrive within the window (RouterOS sends
// them in a burst), reset the timer. When the window expires with no new
// data, resolve with the full accumulated results. For single-block commands
// (the vast majority) the only cost is 20 ms of additional latency;
// all commands still run concurrently on separate tagged channels.
patch(
  path.join(BASE, 'Channel.js'),
  'MULTI_BLOCK',
  [
    {
      find: `if (!this.trapped)\n                    this.emit('done', this.data);\n                this.close();\n                break;`,
      replace: `if (this.trapped) { this.close(); break; }
                if (this._doneTimer) clearTimeout(this._doneTimer);
                this._doneTimer = setTimeout(() => { this._doneTimer = null; this.emit('done', this.data); this.close(); }, 20);
                break;`,
    },
  ]
);

// ── Patch 5: Channel.js — skip channel close for streaming interval commands ──
// The MULTI_BLOCK patch (Patch 4) debounces !done and resolves/closes after
// 20 ms. For ros.stream() channels (this.streaming === true) RouterOS sends
// periodic !done packets between each interval result set. Without this fix
// the 20 ms debounce fires after the first !done, closing the channel and
// preventing all subsequent interval pushes from reaching the listener.
// Fix: when this.streaming is true, treat !done as a continuation marker —
// break without starting the debounce or closing the channel so RouterOS can
// keep delivering data every interval tick.
// NOTE: this patch runs AFTER Patch 4 and targets the content it left behind.
(function patchMultiBlockV2() {
  const channelPath = path.join(BASE, 'Channel.js');
  if (!fs.existsSync(channelPath)) {
    console.warn('[patch] MULTI_BLOCK_V2 — Channel.js not found');
    return;
  }
  let src = fs.readFileSync(channelPath, 'utf8');
  if (src.includes('MIKRODASH_PATCHED_MULTI_BLOCK_V2')) {
    console.log('[patch] MULTI_BLOCK_V2 — already applied, skipping');
    return;
  }
  // Targets the exact two-line sequence left by the MULTI_BLOCK patch.
  // 8-space indent on first line, 16-space indent on second — confirmed via cat -A.
  const find   = `        if (this.trapped) { this.close(); break; }\n                if (this._doneTimer) clearTimeout(this._doneTimer);`;
  const replace = `        if (this.trapped) { this.close(); break; } // MIKRODASH_PATCHED_MULTI_BLOCK_V2\n                if (this.streaming) break;\n                if (this._doneTimer) clearTimeout(this._doneTimer);`;
  if (!src.includes(find)) {
    console.warn('[patch] MULTI_BLOCK_V2 — target not found (MULTI_BLOCK not applied or format changed)');
    return;
  }
  fs.writeFileSync(channelPath, src.replace(find, replace), 'utf8');
  console.log('[patch] MULTI_BLOCK_V2 — applied');
})();

console.log('[patch] Done.');
