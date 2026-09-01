// Run the web tests.
//
// ── WHY A RUNNER AND NOT JUST `node --test` ─────────────────────────────────
//
// These tests are TypeScript, and they exercise the app's own TypeScript by
// bundling it. Node cannot execute either directly, so each test is bundled to
// CommonJS with the esbuild binary the frontend build already depends on, then
// handed to `node --test`.
//
// NO NEW DEPENDENCY, deliberately. `web/package.json` carries esbuild and
// typescript and nothing else; a test runner would be a third. `node:test` is in
// the runtime, and this is the same pattern the differential tests used before
// they moved here.
//
// The bundles go to `web/test-out/`, which is gitignored: they are build output,
// not fixtures, and the retired harness's habit of writing scratch files into
// `testdata/` is exactly what made that directory hard to reason about.
//
// NOT `.test-out`. Node's test runner skips hidden directories, so a dot-prefixed
// output directory matched no test files and Node then tried to load the path
// itself as a module -- a MODULE_NOT_FOUND that says nothing about the cause.
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, readdirSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, '..');
const root = join(web, '..');
const out = join(web, 'test-out');
const esbuild = join(web, 'node_modules', '.bin', 'esbuild');

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const tests = readdirSync(here).filter((f) => f.endsWith('.test.ts')).sort();
if (tests.length === 0) {
  console.error('web tests: no *.test.ts found — the suite is empty, which is not a pass');
  process.exit(1);
}

for (const t of tests) {
  execFileSync(esbuild, [
    join(here, t),
    '--bundle',
    '--format=cjs',
    '--platform=node',
    // The tests read the repository; nothing in node_modules belongs in a bundle.
    '--packages=external',
    '--log-level=error',
    `--outfile=${join(out, t.replace(/\.ts$/, '.cjs'))}`,
  ], { cwd: root, stdio: 'inherit' });
}

// MIKRODASH_ROOT is how a bundled test finds the repository: __dirname inside the
// bundle is `.test-out`, not the test's own directory, so a relative walk would
// silently resolve to the wrong tree.
// THE FILES, NAMED. `node --test <dir>` did not pick these up -- it fell back to
// loading the directory itself as a module and reported MODULE_NOT_FOUND, which
// says nothing about the cause. Listing them is unambiguous and also fails
// loudly if the bundle step produced nothing.
const bundles = readdirSync(out).filter((f) => f.endsWith('.cjs')).map((f) => join(out, f));
if (bundles.length !== tests.length) {
  console.error(`web tests: bundled ${bundles.length} of ${tests.length} tests`);
  process.exit(1);
}
const res = spawnSync(process.execPath, ['--test', ...bundles], {
  cwd: root,
  stdio: 'inherit',
  env: { ...process.env, MIKRODASH_ROOT: root },
});
process.exit(res.status ?? 1);
