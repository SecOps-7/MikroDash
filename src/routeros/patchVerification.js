const path = require('path');

// Which file each marker must appear in. Spelled out rather than derived from
// the marker's name so every compatibility patch is verified in the file it
// actually modifies.
const PATCH_FILES = {
  MIKRODASH_PATCHED_EMPTY_REPLY:     'Channel.js',
  MIKRODASH_PATCHED_EMPTY_NO_CLOSE:  'Channel.js',
  MIKRODASH_PATCHED_UNREGISTEREDTAG: path.join('connector', 'Receiver.js'),
  MIKRODASH_PATCHED_RAW_BYTES:       path.join('connector', 'Receiver.js'),
  MIKRODASH_PATCHED_MULTI_BLOCK:     'Channel.js',
  MIKRODASH_PATCHED_MULTI_BLOCK_V2:  'Channel.js',
  MIKRODASH_PATCHED_UTF8_ENCODE:     path.join('connector', 'Transmitter.js'),
};

const PATCH_MARKERS = Object.keys(PATCH_FILES);

function resolveDistPath(marker) {
  return PATCH_FILES[marker] || path.join('connector', 'Receiver.js');
}

function hasExactPatchMarker(src, marker) {
  const escaped = marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // Markers may be a standalone comment or an inline end-of-line comment.
  // Token boundaries are deliberate: MULTI_BLOCK_V2 must never satisfy the
  // required MULTI_BLOCK marker merely because it shares that prefix.
  return new RegExp(`(?:^|[^A-Za-z0-9_])${escaped}(?![A-Za-z0-9_])`, 'm').test(src);
}

function verifyRouterOSPatchMarkers({
  patchMarkers = PATCH_MARKERS,
  distDir = path.join(__dirname, '..', '..', 'node_modules', 'node-routeros', 'dist'),
  readFileSync,
  log = console,
}) {
  for (const marker of patchMarkers) {
    const target = resolveDistPath(marker);
    const filePath = path.join(distDir, target);
    let src;

    try {
      src = readFileSync(filePath, 'utf8');
    } catch (error) {
      const msg = `[MikroDash] CRITICAL: Could not verify patch "${marker}" in ${target}: ${error.code || error.message}`;
      log.error(msg);
      throw new Error(msg);
    }

    if (!hasExactPatchMarker(src, marker)) {
      const msg = `[MikroDash] CRITICAL: node-routeros patch "${marker}" not found in ${target}`;
      log.error(msg);
      throw new Error(msg);
    }
  }
}

module.exports = {
  PATCH_MARKERS,
  resolveDistPath,
  hasExactPatchMarker,
  verifyRouterOSPatchMarkers,
};
