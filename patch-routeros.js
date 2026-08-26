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
const {
  PATCH_MARKERS,
  resolveDistPath,
  hasExactPatchMarker,
} = require('./src/routeros/patchVerification');

const BASE = path.join(__dirname, 'node_modules', 'node-routeros', 'dist');
let patchFailed = false;

function patch(filePath, description, replacements) {
  if (!fs.existsSync(filePath)) {
    console.warn('[patch] File not found, skipping:', filePath);
    patchFailed = true;
    return false;
  }

  let src = fs.readFileSync(filePath, 'utf8');

  if (hasExactPatchMarker(src, 'MIKRODASH_PATCHED_' + description)) {
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
    console.warn('[patch] Refusing an unverified dependency build. File:', filePath);
    patchFailed = true;
    return false;
  }

  fs.writeFileSync(filePath, src, 'utf8');
  console.log('[patch]', description, '— applied successfully');
  return true;
}

// ── Patch 1: Channel.js — !empty reply ──────────────────────────────────────
// RouterOS 7.18+ sends !empty when a command returns zero results.
// The library throws RosException('UNKNOWNREPLY') on any unknown reply type.
//
// Fix depends on WHICH KIND OF CHANNEL it arrives on, because !empty means two
// different things:
//
//   write()  — one-shot. !empty is the whole answer: the result set is empty.
//              Emit done immediately. On RouterOS 7.23 no !done follows, so
//              waiting for one hangs until the write timeout, and losing that
//              race closes the connection every collector shares.
//
//   stream() — long-running. !empty means "nothing YET". /interface/wifi/
//              frequency-scan sends it within ~6 ms and delivers the real rows
//              about ten seconds later. Closing on it ended the channel before
//              any data arrived; the rows then landed on a tag nobody was
//              listening for and Patch 2 below discarded them silently. The
//              symptom was a scan reporting success with zero results while the
//              identical scan worked in WinBox.
//
// Both halves were verified against a live 7.23.3 hAP AX3: swallowing it for
// write() hangs an empty print for the full timeout and kills the connection;
// closing on it for stream() loses every frequency-scan row.
patch(
  path.join(BASE, 'Channel.js'),
  'EMPTY_REPLY',
  [
    {
      find: `throw new RosException_1.RosException('UNKNOWNREPLY', { reply: reply });`,
      replace: `if (reply === '!empty') { if (this.streaming) return; this.emit('done', []); return; }
        throw new RosException_1.RosException('UNKNOWNREPLY', { reply: reply });`,
    },
    {
      // alternate double-quote form in some builds
      find: `throw new RosException_1.RosException("UNKNOWNREPLY", { reply: reply });`,
      replace: `if (reply === '!empty') { if (this.streaming) return; this.emit('done', []); return; }
        throw new RosException_1.RosException("UNKNOWNREPLY", { reply: reply });`,
    },
  ]
);

// ── Patch 1b: Channel.js — do not CLOSE the channel on !empty ───────────────
// Patch 1 above puts the !empty handling in onUnknown(), which is a listener.
// It never worked for streams, because processPacket does this:
//
//     default:
//         this.emit('unknown', reply);
//         this.close();          // <- runs whatever the listener decided
//         break;
//
// The close happens regardless, so a long-running command that opens with
// !empty ("nothing yet") had its channel torn down within ~10 ms. Traced on a
// live 7.23.3 hAP AX3: /interface/wifi/frequency-scan sends !empty at 12 ms and
// its real results about ten seconds later, by which time the tag was gone and
// Patch 2 below discarded them without a word.
//
// Handle !empty in the default branch itself, and branch on channel kind:
//   streaming  -> swallow, keep the channel open, results are still coming
//   one-shot   -> emit done([]) and close, exactly as before (an empty print
//                 gets no trailing !done on this build, so waiting for one
//                 hangs until the write timeout and kills the shared connection)
patch(
  path.join(BASE, 'Channel.js'),
  'EMPTY_NO_CLOSE',
  [
    {
      find: `            default:\n                this.emit('unknown', reply);\n                this.close();\n                break;`,
      replace: `    default:
                if (reply === '!empty') {
                    if (this.streaming) break;
                    this.emit('done', this.data);
                    this.close();
                    break;
                }
                this.emit('unknown', reply);
                this.close();
                break;`,
    },
    {
      find: `            default:\n                this.emit("unknown", reply);\n                this.close();\n                break;`,
      replace: `    default:
                if (reply === '!empty') {
                    if (this.streaming) break;
                    this.emit('done', this.data);
                    this.close();
                    break;
                }
                this.emit("unknown", reply);
                this.close();
                break;`,
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

// ── Patch 3: Receiver.js — string decoding, per connection ───────────────
// node-routeros hardcodes win1252 when it converts raw TCP bytes to JS strings.
// RouterOS sends UTF-8, so win1252 mangles Cyrillic, Greek and the rest into
// garbage. Decoding as UTF-8 fixes device names, DHCP comments and labels —
// and is what every collector wants.
//
// It is not what a caller reading a FILE wants. `/file/read` returns raw bytes
// in its `data` word, and a UTF-8 decode replaces every invalid byte with
// U+FFFD. A known blob pushed to a live AX3 and read back came home with a
// different sha256 and 177 of its 256 distinct byte values intact.
//
// THIS COMMENT USED TO CLAIM THE LENGTH IS PRESERVED — "one replacement
// character per bad byte, so the reassembled length still matches the file size
// exactly and the result looks fine". THAT IS WRONG, and it was wrong in the
// direction that matters: it describes the corruption as SILENT.
//
// Measured on a 46,910-byte backup: the UTF-8 path returns 46,395 bytes, 515
// short, 1.1%. One-U+FFFD-per-bad-byte accounts for only part of what happens.
// Binary data also contains sequences that are ACCIDENTALLY VALID multi-byte
// UTF-8, and those collapse from 2-4 bytes into a single code point, so the
// length drops rather than holding.
//
// Which means the length check `src/backups/runner.js` already performs
// (`if (out.length !== size) throw`) DOES catch it. The hazard is real and
// severe — 72.85% of bytes differ — but it is not silent, and a reader who
// believed this comment might add a defence that is already there, or distrust
// the one that works. Corrected 2026-08-26 from the port's hardware harness,
// which reconstructed the same file both ways and diffed per byte.
//
// So the encoding becomes a property of the CONNECTION. `this` here is the
// Receiver, one per connection; `rawBytes` is set by src/routeros/client.js
// after connect. latin1 maps each byte to one code unit and is therefore
// lossless — Buffer.from(str, 'latin1') recovers the file exactly.
//
// Nothing sets the flag except the backup transport, which opens its own
// short-lived connection. No collector's data path changes.
patch(
  path.join(BASE, 'connector', 'Receiver.js'),
  'RAW_BYTES',
  [
    // Fresh install: the pristine win1252 form.
    {
      find: `this.currentLine += iconv.decode(data, 'win1252');`,
      replace: `this.currentLine += iconv.decode(data, this.rawBytes ? 'latin1' : 'utf8');`,
    },
    {
      find: `this.currentLine += iconv.decode(data, "win1252");`,
      replace: `this.currentLine += iconv.decode(data, this.rawBytes ? 'latin1' : 'utf8');`,
    },
    {
      // second decode call — handles the case where the buffer contains more
      // data than the current token length requires (sliced into tmpBuffer)
      find: `const tmpStr = iconv.decode(tmpBuffer, 'win1252');`,
      replace: `const tmpStr = iconv.decode(tmpBuffer, this.rawBytes ? 'latin1' : 'utf8');`,
    },
    {
      find: `const tmpStr = iconv.decode(tmpBuffer, "win1252");`,
      replace: `const tmpStr = iconv.decode(tmpBuffer, this.rawBytes ? 'latin1' : 'utf8');`,
    },
    // A tree already carrying the older UTF8_ENCODING patch — a working copy
    // that has not been reinstalled. Without these it would find nothing and
    // warn, leaving the connection unable to read a file.
    {
      find: `this.currentLine += iconv.decode(data, 'utf8');`,
      replace: `this.currentLine += iconv.decode(data, this.rawBytes ? 'latin1' : 'utf8');`,
    },
    {
      find: `const tmpStr = iconv.decode(tmpBuffer, 'utf8');`,
      replace: `const tmpStr = iconv.decode(tmpBuffer, this.rawBytes ? 'latin1' : 'utf8');`,
    },
  ]
);

// ── Patch 4: Channel.js — multi-block !done accumulation ────────────────────
// OBSERVED ON 7.23; NOT REPRODUCIBLE ON 7.24. Read the qualification below
// before treating this patch's cost as justified — it is insurance now, not a
// correctness fix, and the distinction was not recorded when it was written.
//
// The observation: RouterOS wifi-qcom devices (hAP ax2, hAP AX³) send
// /interface/wifi/registration-table/print as SEPARATE response blocks per
// interface, each terminated by its own !done. The library resolves the write()
// Promise on the FIRST !done, so only one interface's clients are returned and
// all subsequent blocks are discarded as UNREGISTEREDTAG packets.
//
// RETESTED ON 7.24 AND IT DID NOT REPRODUCE. The exact hardware — a wifi-qcom
// hAP AX³ running as a CAPsMAN manager, 12 wifi interfaces, one remote CAP, 30
// clients across 8 interfaces — answered in ONE block with ONE !done, six
// repeats out of six, including with the same `=.proplist=` forms wireless.js
// sends. The rows were not even grouped by interface: they came ordered by
// descending client uptime with all 8 interfaces interleaved, which is
// structurally incompatible with a per-interface block. A client returning on
// the first !done got 30 of 30. Widened to 14 further commands on the same
// router, up to a 1000-row /log/print: every one was a single block.
//
// THE PATCH STAYS ANYWAY, deliberately. The original was verified against a
// live 7.23.3 hAP AX3 with specific detail, no 7.23 device is available to
// retest, and MikroDash may still meet one in the field. "Not reproduced on the
// versions we could test" is not "never true", and failing this open means
// silently returning one interface's clients as though they were all of them.
//
// But the cost is now a KNOWN price rather than a free defence: the 20 ms
// debounce below expires on every non-empty one-shot write, because there is
// never a second block to wait for on 7.24. Anyone tempted to make this
// conditional should gate it on the router's RouterOS version rather than
// delete it. Qualification added 2026-08-26 from the port's hardware traces.
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
    patchFailed = true;
    return;
  }
  let src = fs.readFileSync(channelPath, 'utf8');
  if (hasExactPatchMarker(src, 'MIKRODASH_PATCHED_MULTI_BLOCK_V2')) {
    console.log('[patch] MULTI_BLOCK_V2 — already applied, skipping');
    return;
  }
  // Targets the exact two-line sequence left by the MULTI_BLOCK patch.
  // 8-space indent on first line, 16-space indent on second — confirmed via cat -A.
  const find   = `        if (this.trapped) { this.close(); break; }\n                if (this._doneTimer) clearTimeout(this._doneTimer);`;
  const replace = `        if (this.trapped) { this.close(); break; } // MIKRODASH_PATCHED_MULTI_BLOCK_V2\n                if (this.streaming) break;\n                if (this._doneTimer) clearTimeout(this._doneTimer);`;
  if (!src.includes(find)) {
    console.warn('[patch] MULTI_BLOCK_V2 — target not found (MULTI_BLOCK not applied or format changed)');
    patchFailed = true;
    return;
  }
  fs.writeFileSync(channelPath, src.replace(find, replace), 'utf8');
  console.log('[patch] MULTI_BLOCK_V2 — applied');
})();

// ── Patch 6: Transmitter.js — UTF-8 string encoding ─────────────────────────
// The mirror of Patch 3, and it was missed when that one landed: node-routeros
// still ENCODES outgoing words as win1252. Anything outside that set has no
// representation, so iconv substitutes '?' — silently, before the word reaches
// the router.
//
// Reading was fixed and writing was not, which stopped mattering only because
// MikroDash could not write. It can now: a Cyrillic comment typed into the
// resource engine was stored on a live AX3 as '???????'. Verified there, not
// inferred.
//
// RouterOS speaks UTF-8 in both directions (WinBox sends it too), and the
// length descriptor is computed from `encoded.length` — a byte count — so
// multi-byte characters are framed correctly with no further change.
patch(
  path.join(BASE, 'connector', 'Transmitter.js'),
  'UTF8_ENCODE',
  [
    {
      find: `const encoded = iconv.encode(str, 'win1252');`,
      replace: `const encoded = iconv.encode(str, 'utf8');`,
    },
    {
      find: `const encoded = iconv.encode(str, "win1252");`,
      replace: `const encoded = iconv.encode(str, 'utf8');`,
    },
  ]
);

// A successful npm install is not enough: this archived dependency is safe for
// MikroDash only with every required compatibility patch. Docker builds and CI
// must fail closed if a dependency update makes a marker or behavior disappear.
const required = PATCH_MARKERS.map(marker => [path.join(BASE, resolveDistPath(marker)), marker]);
for (const [file, marker] of required) {
  let source = '';
  try { source = fs.readFileSync(file, 'utf8'); } catch (_) { /* handled below */ }
  if (!hasExactPatchMarker(source, marker)) {
    console.error('[patch] REQUIRED marker missing:', marker, 'in', file);
    patchFailed = true;
  }
}
const channelSource = fs.existsSync(path.join(BASE, 'Channel.js'))
  ? fs.readFileSync(path.join(BASE, 'Channel.js'), 'utf8') : '';
const emptyReplySafe = channelSource.includes(
  "if (reply === '!empty') { if (this.streaming) return; this.emit('done', []); return; }"
);
const emptyDefaultSafe = /if \(reply === '!empty'\) \{\s*if \(this\.streaming\) break;\s*this\.emit\('done', this\.data\);\s*this\.close\(\);\s*break;/m
  .test(channelSource);
if (!emptyReplySafe || !emptyDefaultSafe || !channelSource.includes('if (this.streaming) break;')) {
  console.error('[patch] REQUIRED patched behavior verification failed');
  patchFailed = true;
}
if (patchFailed) {
  console.error('[patch] FAILED — refusing to continue with an unverified node-routeros build.');
  process.exitCode = 1;
} else {
  console.log('[patch] Done — all required markers and behaviors verified.');
}
