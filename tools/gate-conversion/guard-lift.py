#!/usr/bin/env python3
"""Guard a gate's LIFT-VALIDITY assertions, driven by which one actually fires.

    python3 tools/gate-conversion/guard-lift.py <gate-name>

A lift-validity assertion asks the live SOURCE a question — "is the handler still
at this anchor", "did the slice keep its element" — and is unanswerable once the
source is gone. It must be guarded. A COMPARISON must never be (LOOP.md 3n).

Rather than guess which assertions are which from their text, this runs the gate
WITHOUT a reference, reads the message of the assertion that actually fired,
guards that one, and repeats. It stops when the gate stops failing on an
assertion — at which point what remains is the real work: freezing the live runs.

It refuses to guard an assertion whose statement contains a comparison call
(`cmp(`), and it never guards more than `-max` of them, because a gate where
every assertion is a live-source question is a gate that would be left checking
nothing.
"""
import io, os, re, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GUARD = 'if (LIFT.hasReference(ROOT)) '

def run(gate):
    p = subprocess.run(['node', os.path.join('tools', gate + '.js')], cwd=ROOT,
                       capture_output=True, text=True,
                       env=dict(os.environ, MIKRODASH_SRC='/nonexistent'))
    return p.returncode, (p.stdout or '') + (p.stderr or '')

def run_with_ref(gate):
    p = subprocess.run(['node', os.path.join('tools', gate + '.js')], cwd=ROOT,
                       capture_output=True, text=True,
                       env=dict(os.environ,
                                MIKRODASH_SRC=os.environ.get('MIKRODASH_SRC_REAL', '../MikroDash')))
    return p.returncode, (p.stdout or '') + (p.stderr or '')


def counts(out):
    """Every count the gate prints — its own claim about how much it checked."""
    return tuple(int(x) for x in re.findall(
        r'\b(\d+)\s+(?:cases?|comparisons?|steps?|tabs?)\b', out))


def fired_message(out):
    m = re.search(r'AssertionError \[ERR_ASSERTION\]: (.+)', out)
    return m.group(1).strip() if m else None

def guard_by_message(path, msg):
    s = io.open(path, encoding='utf-8').read()
    lines = s.split('\n')
    # The message may be built by concatenation; match on a distinctive prefix.
    needle = msg[:40]
    hit = next((i for i, l in enumerate(lines) if needle in l), None)
    if hit is None:
        for frag in (msg[:24], msg.split(' — ')[0][:24]):
            hit = next((i for i, l in enumerate(lines) if frag in l), None)
            if hit is not None:
                break
    if hit is None:
        return None, 'message not found in source'
    # Walk BACK to the statement that opens this assert.
    start = hit
    while start >= 0 and not re.match(r'^\s*(assert|if \()', lines[start]):
        start -= 1
    if start < 0:
        return None, 'no assert statement found above the message'
    if lines[start].lstrip().startswith('if ('):
        return None, 'already guarded'
    stmt_end = hit
    while stmt_end < len(lines) and ');' not in lines[stmt_end]:
        stmt_end += 1
    stmt = '\n'.join(lines[start:stmt_end + 1])
    if re.search(r'(?<![\w.])cmp\(', stmt):
        return None, 'REFUSED: the statement contains a comparison'
    # ONLY A QUESTION ABOUT THE SOURCE TEXT MAY BE GUARDED.
    #
    # An assertion about the RESULT of running the lifted code — `s['x'].text ===
    # '110 / 512'` — is a BELIEVABILITY check, and guarding one silently deletes
    # it. Those must be re-aimed at the port instead, which is a judgement this
    # script cannot make. Left to a human, reported rather than guessed.
    #
    # Without this, iputil-card had four believability assertions guarded away in
    # one run, and the gate would have gone green having stopped checking that a
    # gauge renders at all.
    # `\w+At\b` is the anchor-INDEX convention these gates use (`iifeAt`,
    # `handlerAt`) — `assert.ok(at > iifeAt, …)` is a source question and the
    # first draft of this regex missed it.
    if not re.search(r'\.includes\(|\.indexOf\(|\bsrc\b|Src\b|\bbody\b|\w*At\b', stmt):
        return None, ('REFUSED: not a question about the source text - this looks '
                      'like a believability assertion and must be RE-AIMED at the '
                      'port, not guarded')
    indent = re.match(r'^(\s*)', lines[start]).group(1)
    lines[start] = indent + GUARD + lines[start].lstrip()
    io.open(path, 'w', encoding='utf-8').write('\n'.join(lines))
    return lines[start].strip()[:78], None

def main():
    gate = sys.argv[1]
    mx = int(sys.argv[sys.argv.index('-max') + 1]) if '-max' in sys.argv else 8
    path = os.path.join(ROOT, 'tools', gate + '.js')
    src0 = io.open(path, encoding='utf-8').read()
    if 'LIFT' not in src0:
        print('%s: no LIFT binding - route its read first' % gate)
        return 2
    n = 0
    while n < mx:
        rc, out = run(gate)
        if rc == 0:
            # EXIT 0 IS NOT ENOUGH. A gate whose comparisons all sat behind the
            # thing being guarded exits 0 having compared NOTHING, and prints
            # nothing because it never reached its summary. `sched-runs-check` did
            # exactly that and an earlier version of this script called it a pass.
            _, out2 = run_with_ref(gate)
            n1, n2 = counts(out), counts(out2)
            if not out.strip():
                print('%s: exits 0 but prints NOTHING - it never reached its summary, '
                      'so it compared nothing. NOT converted.' % gate)
                return 1
            if n2 and n1 != n2:
                print('%s: exits 0 but reports %s without a reference vs %s with one '
                      '- it is checking less. NOT converted.' % (gate, n1, n2))
                return 1
            print('%s: passes without a reference after guarding %d (reports %s, same as with)'
                  % (gate, n, n1))
            return 0
        msg = fired_message(out)
        if not msg:
            print('%s: guarded %d; now fails for a NON-ASSERTION reason '
                  '(this is the freezing work)' % (gate, n))
            return 1
        line, err = guard_by_message(path, msg)
        if err:
            print('%s: stopped after %d - %s (%r)' % (gate, n, err, msg[:50]))
            return 1
        n += 1
        print('   guarded: %s' % line)
    print('%s: hit the -max %d limit; guarding stopped' % (gate, mx))
    return 1

sys.exit(main())
