#!/usr/bin/env python3
"""Freeze a gate's LIFTED SOURCE, so its live half still runs without a reference.

    python3 tools/gate-conversion/freeze-src.py <gate-name>

WHY THE SOURCE AND NOT THE OUTPUTS
----------------------------------
Where a gate builds its live half by executing lifted text — `vm.runInContext`,
`new Function` — freezing the TEXT is strictly better than freezing each answer:

  * the recording is a fraction of the size (traffic-buffer: 7 entries, not 84);
  * the live half still EXECUTES, so a case added later still gets a live answer;
  * assertions about the lifted text then validate the RECORDING, so they stay
    unguarded and keep earning their place.

So this rewrites every module-scope `const X = <lifter>(...)` into
`const X = G.value('X', () => <lifter>(...))`, and adds a non-emptiness assertion
for each — because a golden holding empty strings would let every comparison pass
vacuously, which is the failure this whole exercise exists to avoid.

It only touches assignments whose right-hand side is a single call to a known
lifting helper, and it never touches one already wrapped.
"""
import io, os, re, sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
LIFTERS = ('slice', 'sliceNamed', 'lineWith', 'grab', 'lift', 'braceBody', 'region', 'handler')


def local_lifters(s):
    """Every function in THIS gate that reads the live source.

    The fixed list above covers the common names, but each gate tends to define
    its own — `liftBlock`, `pick`, `cut`. They are recognisable rather than
    guessable: a function whose body indexes or slices `src`. Detecting them is
    what let the last dozen gates convert without a bespoke edit each.
    """
    out = set()
    for m in re.finditer(r'(?:^function (\w+)\s*\(|^const (\w+) = \([^)]*\) =>|'
                         r'^const (\w+) = function)', s, re.M):
        name = m.group(1) or m.group(2) or m.group(3)
        body = s[m.start(): m.start() + 1200]
        if re.search(r'\bsrc\.(slice|indexOf|match|split)\b', body):
            out.add(name)
    return tuple(sorted(out))

def main():
    gate = sys.argv[1]
    p = os.path.join(ROOT, 'tools', gate + '.js')
    s = io.open(p, encoding='utf-8').read()
    if 'LIFT' not in s:
        print('%s: no LIFT binding — route its read first' % gate)
        return 2
    if ".golden(" not in s:
        m = re.search(r"const LIFT = require\('\./lib/lift\.js'\);", s)
        if not m:
            print('%s: cannot find the LIFT require' % gate)
            return 2
        s = s[:m.end()] + ("\nconst G = LIFT.golden('%s');" % gate) + s[m.end():]

    lifters = tuple(sorted(set(LIFTERS) | set(local_lifters(s))))
    # MULTI-LINE CALLS TOO: `const X = lift(\n  'decl', …\n);` is the same thing
    # wrapped, and matching only single-line calls left several gates untouched.
    pat = re.compile(r'^const (\w+) = ((?:%s)\((?:[^;]|\n)*?\));$' % '|'.join(lifters), re.M)
    names = []
    def sub(m):
        name, call = m.group(1), m.group(2)
        names.append(name)
        return "const %s = G.value('%s', () => %s);" % (name, name, call)
    s, n = pat.subn(sub, s)

    # THE BARE FORM: `const X = src.slice(i, j);`, with the anchor indexes and
    # their `=== -1` assertions on separate lines above. This is what the last
    # dozen gates actually use, and no lifter name appears anywhere in it.
    # `[^;]` ALREADY MATCHES A NEWLINE, so the old `(?:[^;]|\n)*?` had two ways
    # to match the same character. That ambiguity is what makes the engine
    # backtrack polynomially on a long non-matching line; the alternation added
    # nothing to what it accepted.
    bare = re.compile(r'^const (\w+) = (src\.slice\([^;]*?\));$', re.M)
    def sub_bare(m):
        name, call = m.group(1), m.group(2)
        names.append(name)
        return "const %s = G.value('%s', () => %s);" % (name, name, call)
    s, n2 = bare.subn(sub_bare, s)
    n += n2

    # And the ANCHOR ASSERTIONS those slices depend on are questions about the
    # SOURCE — guard, never freeze (LOOP.md 3ab).
    # ANCHOR-ASSERTION SHAPES, all of them questions about the source. The list
    # grew one gate at a time; the common thread is an assertion on an INDEX
    # derived from `src.indexOf`, whatever the comparison.
    for pat_a in (r'^(\s*if \((\w+) === -1\) \{?)', r'^(\s*assert\.ok\((\w+) > 0,)',
                  r'^(\s*if \((\w+) < 0\) \{?)',
                  r'^(\s*assert\.ok\((\w+) > (?:\w+) &&)',
                  r'^(\s*assert\.ok\((\w+) >= 0,)'):
        def _g(m):
            head = m.group(1)
            if 'hasReference' in head:
                return head
            ind = re.match(r'^(\s*)', head).group(1)
            return ind + 'if (LIFT.hasReference(ROOT)) ' + head.lstrip()
        s = re.sub(pat_a, _g, s, flags=re.M)

    # IIFE-FORM LIFTS. `const escSrc = (() => { … src.slice(…) … })();` is the
    # same thing written differently, and the first version of this tool skipped
    # them — so the gate still threw on the very first one it met. Only IIFEs that
    # actually read `src` are touched; the rest are ordinary gate code.
    # SINGLE-LINE IIFEs too — `const escSrc = (() => { … })();` on one line. The
    # multi-line pattern below requires a newline after the brace and skipped
    # these entirely, so two gates still threw `dcEsc is not defined`.
    one = re.compile(r'^const (\w+) = \(\(\) => \{.*\}\)\(\);$', re.M)
    while True:
        m = one.search(s)
        if not m or 'src' not in m.group(0) or 'G.value' in m.group(0):
            break
        var, body = m.group(1), m.group(0)
        inner = body[body.index('{') + 1: body.rindex('}')]
        s = s.replace(body, "const %s = G.value('%s', () => {%s});" % (var, var, inner), 1)
        names.append(var)
        n += 1

    while True:
        m = re.search(r'^const (\w+) = \(\(\) => \{\n(?:.*\n)*?\}\)\(\);$', s, re.M)
        if not m or 'src' not in m.group(0) or "G.value" in m.group(0):
            break
        var, body = m.group(1), m.group(0)
        inner = body[body.index('{') + 1: body.rindex('}')]
        s = s.replace(body, "const %s = G.value('%s', () => {%s});" % (var, var, inner), 1)
        names.append(var)
        n += 1
    if not n:
        print('%s: no module-scope lifted assignments found' % gate)
        return 1

    # A non-emptiness check per frozen name, inserted after the LAST assignment.
    #
    # Finding that point by scanning for the next `;` put the block INSIDE a
    # multi-line IIFE body — `Cannot access 'escSrc' before initialization`. So
    # the end is found by balancing brackets from the assignment's start.
    last = max(s.index("const %s = G.value(" % nm) for nm in names)
    depth, end, instr, q = 0, None, False, ''
    for i in range(last, len(s)):
        c = s[i]
        if instr:
            if c == '\\':
                continue
            if c == q:
                instr = False
            continue
        if c in '"\'`':
            instr, q = True, c
        elif c in '([{':
            depth += 1
        elif c in ')]}':
            depth -= 1
            if depth == 0:
                end = s.index('\n', i)
                break
    assert end is not None, 'could not find the end of the last frozen assignment'
    # A PLAIN THROW, not `assert`. Several gates do not require node:assert, and
    # emitting `assert.ok` into one produced `ReferenceError: assert is not
    # defined` — a tool that breaks the gate it is converting.
    guard = ("\n// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every\n"
             "// comparison below pass while comparing nothing, which is the exact failure\n"
             "// this conversion exists to avoid.\n"
             "for (const [__n, __v] of %s) {\n"
             "  if (typeof __v !== 'string' || __v.length <= 4) {\n"
             "    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');\n"
             "  }\n"
             "}" % ('[' + ', '.join("['%s', %s]" % (nm, nm) for nm in names) + ']'))
    s = s[:end] + guard + s[end:]
    io.open(p, 'w', encoding='utf-8').write(s)
    print('%s: froze %d lifted source(s): %s' % (gate, n, ', '.join(names)))
    return 0

sys.exit(main())
