const path = require('path');

// Which file each marker must appear in. Spelled out rather than derived from
// the marker's name: the mapping used to be a substring test, and it worked
// only because every marker but one lived in Receiver.js. Patch 6 put a marker
// in Transmitter.js, which a substring test has no way to see.
const PATCH_FILES = {
  MIKRODASH_PATCHED_EMPTY_REPLY:     'Channel.js',
  MIKRODASH_PATCHED_UNREGISTEREDTAG: path.join('connector', 'Receiver.js'),
  MIKRODASH_PATCHED_RAW_BYTES:       path.join('connector', 'Receiver.js'),
  MIKRODASH_PATCHED_UTF8_ENCODE:     path.join('connector', 'Transmitter.js'),
};

const PATCH_MARKERS = Object.keys(PATCH_FILES);

function resolveDistPath(marker) {
  return PATCH_FILES[marker] || path.join('connector', 'Receiver.js');
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

    if (!src.includes(marker)) {
      const msg = `[MikroDash] CRITICAL: node-routeros patch "${marker}" not found in ${target}`;
      log.error(msg);
      throw new Error(msg);
    }
  }
}

module.exports = {
  PATCH_MARKERS,
  resolveDistPath,
  verifyRouterOSPatchMarkers,
};
