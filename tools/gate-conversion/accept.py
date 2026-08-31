#!/usr/bin/env python3
"""The four acceptance tests a converted gate must pass, run as one command.

    python3 tools/gate-conversion/accept.py <gate-name> [file:line old new ...]

Mutations are given as GROUPS OF THREE arguments — a `file:line` location, the
text to replace, and its replacement. They were once one colon-joined argument,
which silently mangled any mutation whose text contained a colon; the first one
that did was `socket.emit('firewall:tab', tab)`, and it presented as a build
crash rather than as a bad argument.

WHY THIS IS A TOOL AND NOT A HABIT
----------------------------------
Three of the first six mutations tried by hand this session were misread, each
in a different way, and every one of them looked like a PASSING acceptance test:

  1. The anchor did not exist            -> `replace` renamed nothing.
  2. The anchor existed IN A COMMENT     -> renamed prose, not code.
  3. The mutation did not compile        -> esbuild crashed, exit was non-zero,
                                            which reads exactly like a detection.

So this refuses to report a mutation as caught unless it (a) landed on the named
line, (b) changed that line, (c) did not crash the build, and (d) failed. Each of
those is checked separately and reported separately.

The fourth test corrupts EVERY golden entry, not the first — a corrupter that
only touches entry one cannot distinguish a gate that checks everything from one
that checks the first case and stops.
"""
import json, io, os, re, subprocess, sys, shutil, tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GOLD = os.path.join(ROOT, 'testdata', 'golden-gates')
NOREF = dict(os.environ, MIKRODASH_SRC='/nonexistent')
WITHREF = dict(os.environ, MIKRODASH_SRC=os.environ.get('MIKRODASH_SRC_REAL', '../MikroDash'))

def run(gate, env):
    p = subprocess.run(['node', os.path.join('tools', gate + '.js')], cwd=ROOT,
                       capture_output=True, text=True, env=env)
    return p.returncode, (p.stdout or '') + (p.stderr or '')

def gate_inputs(gate):
    """The set of source files the gate's bundle actually pulls in.

    THE FIFTH MUTATION TRAP. A mutation can apply, compile, and change nothing —
    because it is in a module the gate never loads. It then reads as NOT CAUGHT
    and looks exactly like a coverage gap in the gate.

    That happened with map-tooltip-check: `dashboard-card-map.ts` was mutated for
    a gate that bundles `connections-worldmap.ts`. It was found by reading the
    golden, which is not a method that scales.

    So: re-run the gate's own esbuild invocation with --metafile and read the
    inputs. Returns None when the gate has no esbuild step or it cannot be
    parsed — in which case the check is skipped rather than guessed at.
    """
    src = io.open(os.path.join(ROOT, 'tools', gate + '.js'), encoding='utf-8').read()
    m = re.search(r"execFileSync\((.{0,4000}?)\{ stdio", src, re.S)
    if not m:
        return None
    blob = m.group(1)
    # The entry is either a literal path.join(...) or a written temp ENTRY file.
    ents = re.findall(r"path\.join\(ROOT, 'web', 'src'(?:, '[^']+')+\)", blob)
    entry = None
    if ents:
        parts = re.findall(r"'([^']+)'", ents[0])
        entry = os.path.join(ROOT, *parts)
    else:
        w = re.search(r"fs\.writeFileSync\(ENTRY,\s*(.*?)\);", src, re.S)
        if w:
            mod = re.search(r"\.\./web/src/([A-Za-z0-9/_.-]+)", w.group(1))
            if mod:
                base = os.path.join(ROOT, 'web', 'src', mod.group(1))
                for cand in (base, base.replace('.js', '.ts'), base + '.ts'):
                    if os.path.exists(cand):
                        entry = cand
                        break
    if not entry or not os.path.exists(entry):
        return None
    meta = os.path.join(ROOT, 'testdata', '.accept-meta.json')
    try:
        subprocess.run([os.path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
                        entry, '--bundle', '--format=cjs', '--platform=node',
                        '--outfile=/dev/null', '--metafile=' + meta, '--log-level=silent'],
                       cwd=ROOT, capture_output=True, check=True)
        d = json.load(io.open(meta))
        return {os.path.normpath(k) for k in d.get('inputs', {})}
    except Exception:
        return None
    finally:
        try:
            os.remove(meta)
        except OSError:
            pass


def crashed(out):
    """A BUILD failure, as distinct from the gate detecting the mutation.

    The first version of this matched `Node.js v\d` — the footer Node prints on
    ANY uncaught throw. But a gate that catches a mutation with `assert` throws an
    AssertionError, which prints that same footer, so a real detection was being
    reported as "CRASHED (not a test)". Found by feeding it a mutation the gate
    genuinely caught.

    So: an assertion failure is a DETECTION and never a crash. Only a failure to
    build or load counts.
    """
    if re.search(r'ERR_ASSERTION|AssertionError', out):
        return False
    return bool(re.search(r'esbuild|error: |SyntaxError|MODULE_NOT_FOUND|'
                          r'Cannot find module|Transform failed', out))

def corrupt(v):
    if isinstance(v, bool): return not v
    if isinstance(v, (int, float)): return v + 9999
    if isinstance(v, str):
        # A recording is often a JSON document in a string; corrupt INSIDE it so
        # the gate's own parse still succeeds and the comparison is what fails.
        try:
            inner = json.loads(v)
        except Exception:
            return v + '__CORRUPT__'
        return json.dumps(corrupt(inner))
    if isinstance(v, list): return [corrupt(x) for x in v] + ['__CORRUPT__']
    if isinstance(v, dict): return {k: corrupt(x) for k, x in v.items()}
    return '__CORRUPT__'

def main():
    gate = sys.argv[1]
    muts = sys.argv[2:]
    ok = True

    print('== 1. passes WITH the reference')
    rc, out = run(gate, WITHREF)
    print('   exit=%d %s' % (rc, '' if rc == 0 else '<- FAIL'))
    ok &= rc == 0

    print('== 2. passes WITHOUT the reference')
    rc, out = run(gate, NOREF)
    print('   exit=%d %s' % (rc, '' if rc == 0 else '<- FAIL'))
    print('   %s' % out.strip().split('\n')[-1][:100])
    ok &= rc == 0

    print('== 3. port mutations still fail WITHOUT the reference')
    # TEST 3 IS MEANINGLESS IF TEST 2 FAILED. A gate that already throws without a
    # reference "fails" under every mutation, including equivalent ones — so every
    # mutation reports as caught and the list looks stronger than it is. Seen for
    # real on view-preset-check: a list-reorder mutation read as caught while the
    # gate was dying at module scope, then correctly read as NOT caught once the
    # gate passed. Refuse to draw the conclusion rather than draw a false one.
    if not ok:
        print('   SKIPPED - test 2 failed, so every mutation would "fail" trivially')
        print('\n%s: NOT ACCEPTED' % gate)
        return 1
    if not muts:
        print('   NO MUTATIONS GIVEN - this is not a pass, it is an untested gate')
        ok = False
    if len(muts) % 3:
        print('mutations come in groups of three: <file:line> <old> <new>')
        return 2
    inputs = gate_inputs(gate)
    if inputs is not None:
        print('   (gate bundles %d source file(s))' % len(inputs))
    for gi in range(0, len(muts), 3):
        loc, old, new = muts[gi], muts[gi + 1], muts[gi + 2]
        f, _, ln = loc.rpartition(':')
        if inputs is not None and os.path.normpath(f) not in inputs:
            print('   WRONG MODULE %s - the gate does not bundle it <- NOT A TEST' % f)
            ok = False
            continue
        path = os.path.join(ROOT, f)
        src = io.open(path, encoding='utf-8').read()
        lines = src.split('\n')
        i = int(ln) - 1
        if old not in lines[i]:
            print('   ANCHOR MISSED %s:%s (%r not on that line) <- NOT A TEST' % (f, ln, old))
            ok = False
            continue
        before = lines[i]
        lines[i] = lines[i].replace(old, new, 1)
        if lines[i] == before:
            print('   NO CHANGE %s:%s <- NOT A TEST' % (f, ln))
            ok = False
            continue
        io.open(path, 'w', encoding='utf-8').write('\n'.join(lines))
        try:
            rc, out = run(gate, NOREF)
            if crashed(out):
                print('   %s:%s CRASHED (build error, not a detection) <- NOT A TEST' % (f, ln))
                print('      %s' % lines[i].strip()[:80])
                ok = False
            elif rc == 0:
                print('   %s:%s NOT CAUGHT <- FAIL' % (f, ln))
                print('      %s' % lines[i].strip()[:80])
                ok = False
            else:
                print('   caught: %s' % lines[i].strip()[:80])
        finally:
            io.open(path, 'w', encoding='utf-8').write(src)

    print('== 4. a fully corrupted golden is noticed')
    gp = os.path.join(GOLD, gate + '.json')
    if not os.path.exists(gp):
        print('   no golden (gate freezes nothing) - test does not apply')
    else:
        orig = io.open(gp, encoding='utf-8').read()
        d = json.loads(orig)
        for k in d:
            d[k] = corrupt(d[k])
        json.dump(d, io.open(gp, 'w'))
        try:
            rc, out = run(gate, NOREF)
            print('   corrupted all %d entries -> exit=%d %s' % (len(d), rc, '' if rc else '<- FAIL'))
            ok &= rc != 0
        finally:
            io.open(gp, 'w', encoding='utf-8').write(orig)

    print('\n%s: %s' % (gate, 'ACCEPTED' if ok else 'NOT ACCEPTED'))
    return 0 if ok else 1

sys.exit(main())
