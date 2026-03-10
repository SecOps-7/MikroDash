const path = require('path');

const PATCH_MARKERS = ['MIKRODASH_PATCHED_EMPTY_REPLY', 'MIKRODASH_PATCHED_UNREGISTEREDTAG'];

function resolveDistPath(marker) {
  return marker.includes('EMPTY') ? 'Channel.js' : path.join('connector', 'Receiver.js');
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
