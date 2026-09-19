// Parse TypeScript source for the tests that ask structural questions
// ("does switchRouter call resetSysMeta?").
//
// ── WHY THIS IS NOT `ts.createSourceFile` ANY MORE ──────────────────────────
//
// TypeScript 7 is the native compiler, and its package no longer exports the
// JavaScript compiler API. The parser is reachable only through
// `typescript/unstable/sync`, a client that starts the native compiler and
// talks to it. `unstable` is meant: a 7.x release may change it, and these
// tests are what would say so, by failing to load rather than passing.
//
// ── A VIRTUAL PROJECT, SO EVERY CALLER PARSES THE SAME WAY ──────────────────
//
// The API parses the files of a project, not a string. The caller passes the
// texts it wants parsed, real files it read or a probe it made up, and they
// become the whole of a project on a virtual file system with no lib, so
// nothing on disk decides what is parsed. The nodes stay readable after the
// compiler is closed.

const { API } = require('typescript/unstable/sync');
const { createVirtualFileSystem } = require('typescript/unstable/fs');
export const ast = require('typescript/unstable/ast');
export const is = require('typescript/unstable/ast/is');

const ROOT = '/parse';

/** Parses each text; the result is keyed by the name it was passed under. */
export function parseSources(texts: Record<string, string>): Record<string, any> {
  const names = Object.keys(texts);
  const at = (n: string) => ROOT + '/' + String(names.indexOf(n)) + '.ts';
  const vfs: Record<string, string> = {};
  for (const n of names) vfs[at(n)] = texts[n] as string;
  vfs[ROOT + '/tsconfig.json'] = JSON.stringify({
    compilerOptions: { noLib: true, noEmit: true, types: [] },
    files: names.map(at),
  });
  const api = new API({ cwd: ROOT, fs: createVirtualFileSystem(vfs) });
  try {
    const program = api.updateSnapshot({ openProject: ROOT + '/tsconfig.json' })
      .getProject(ROOT + '/tsconfig.json').program;
    const out: Record<string, any> = {};
    for (const n of names) {
      const sf = program.getSourceFile(at(n));
      if (!sf) throw new Error('ts-parse: the compiler returned no source file for ' + n);
      out[n] = sf;
    }
    return out;
  } finally {
    api.close();
  }
}

/** Parses one text. */
export function parseSource(name: string, text: string): any {
  return parseSources({ [name]: text })[name];
}
