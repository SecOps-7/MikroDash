# Gate conversion helper — freezing a differential gate off `../MikroDash`.
#
# Run it, then finish by hand. It does the mechanical three-quarters:
#
#   1. Routes every reference read through `L.liveSource`, which returns '' when
#      the reference is absent instead of throwing ENOENT at module load.
#   2. Freezes module-scope `const X = L.<lifter>(...)` values.
#   3. Wraps `runLive` / `liveRun` / `liveRunner` / `framesLive` calls in
#      `G.live(G.seq(), ...)`.
#
# WHAT IT CANNOT DO, and what you will therefore do by hand:
#
#   - Wrap a live context called by its OWN method names (`c.openAccountModal`,
#     `api.startDrag`, `L.enterEditMode`). There is no name to match on.
#   - Guard module-scope asserts that validate the LIFT. Use
#     `L.hasReference(ROOT)` — guarding is right, freezing the lifted TEXT is not,
#     because that puts the reference's JavaScript in testdata/.
#   - Freeze a STATEFUL live run. Replay the whole run inside ONE `G.value` and
#     return the ordered sequence; the port loop then indexes into it.
#
# THREE COLLISIONS IT DOES HANDLE, each found by a gate failing WITH the
# reference — which is the loud, cheap failure to design for:
#
#   - `G` already taken  -> binds the golden to `__GOLD`.
#   - `L` already taken  -> binds the lifter to `LIFT`. grid-drag-check uses `L`
#     for its live side, so `L.hasReference(...)` there resolved to the live side.
#   - The inserted require must go BEFORE its first use, not after the last
#     existing require.
#
# It routes `LIVE` and `SRC` but never `ROOT`: 14 gates use `ROOT` for the PORT's
# own root, and routing it would redirect reads of our own testdata.
#
# Usage:  python3 tools/gate-conversion/convert.py tools/<gate>.js

import io, re, sys, os

def match_paren(s, open_idx):
    depth, i, n = 0, open_idx, len(s)
    instr, q = False, ''
    while i < n:
        c = s[i]
        if instr:
            if c == '\\': i += 2; continue
            if c == q: instr = False
        elif c in '"\'`':
            instr, q = True, c
        elif c == '(':
            depth += 1
        elif c == ')':
            depth -= 1
            if depth == 0: return i + 1
        i += 1
    raise ValueError('unbalanced')

def convert(path):
    name = os.path.basename(path)[:-3]
    s = io.open(path, encoding='utf-8').read()
    if 'L.golden(' in s: return 'already converted'
    # `L` MAY ALREADY MEAN SOMETHING ELSE. grid-drag-check binds it to its live
    # side (`L.api`, `L.w`, `L.handle`), so inserting `const L = require(...)`
    # collided — and any guard written as `L.hasReference(...)` then resolved to
    # the live side instead of the module. Same shape as the G -> __GOLD check.
    lv = 'L'
    if re.search(r'\b(const|let|var)\s+L\b(?!\s*=\s*require)', s) or re.search(r'\bL\.(api|w|handle)\b', s):
        lv = 'LIFT'
    # BATCH TWO reads app.js itself. Route it through the shared seam, which
    # returns '' when the reference is absent instead of throwing at require.
    # EVERY reference read, not just app.js. Five gates read
    # public/js/dashboard-grid.js, public/index.html, src/*.js and so on, and
    # routing only app.js left them dying on ENOENT without the reference.
    # `liveSource(root, rel)` already takes a relative path.
    routed = False
    def _route(m):
        nonlocal routed
        routed = True
        parts = m.group(1).strip()
        return (lv + ".liveSource(ROOT, path.join(" + parts + "))")
    s2 = re.sub(r"fs\.readFileSync\(path\.join\((?:LIVE|SRC),([^)]*)\), *'utf8'\)", _route, s)
    s = s2

    req = "const " + lv + " = require('./lib/lift.js');"
    if req not in s:
        if not routed: return 'SKIP: no lift require and no direct app.js read'
        # IMMEDIATELY BEFORE THE FIRST USE. Inserting after the last top-level
        # require put `const L = ...` AFTER the liveSource call in two gates —
        # "Cannot access 'L' before initialization". The line that used to read
        # app.js is a safe anchor: it referenced LIVE, which is derived from
        # ROOT, so both are already defined there.
        lines = s.split('\n')
        use = next(i for i, l in enumerate(lines) if (lv + '.liveSource(') in l)
        lines.insert(use, req)
        s = '\n'.join(lines)

    # A gate may already use `G` — rosusers-page-check does, and the collision was
    # a SyntaxError visible only when the gate ran WITH the reference.
    gv = '__GOLD' if re.search(r'\b(const|let|var)\s+G\b', s) else 'G'

    header = ("\n// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()\n"
              "// in lib/lift.js. Re-freeze with: node tools/" + name + ".js --freeze\n"
              "const " + gv + " = " + lv + ".golden('" + name + "');")
    s = s.replace(req, req + header, 1)

    # 1. Module-scope lifted consts, frozen AT THE LIFT.
    spans = []
    for m in re.finditer(r'^const ([A-Za-z_][\w]*) = (' + lv + r'\.(?:region|handler|whole|line|idsFor|fileScopeEls|fileScopeVars|regionEls))\(', s, re.M):
        spans.append((m.start(), match_paren(s, m.end() - 1), m.group(1)))
    n_consts = 0
    for start, end, cname in reversed(spans):
        text = s[start:end]
        rhs = text[text.index('=') + 1:].strip()
        s = s[:start] + "const " + cname + " = " + gv + ".value('" + cname + "', () => " + rhs + ")" + s[end:]
        n_consts += 1

    # 2. live-side calls, frozen as outputs.
    spans, auto = [], 0
    for m in re.finditer(r'\b(runLive|liveRun|liveRunner|framesLive)\(', s):
        line_start = s.rfind('\n', 0, m.start()) + 1
        line = s[line_start:s.index('\n', m.start())]
        st = line.lstrip()
        if st.startswith('function ') or st.startswith('async function ') or '.live(' in line:
            continue
        spans.append((m.start(), match_paren(s, m.end() - 1), line))
    n_live = len(spans)
    for start, end, line in reversed(spans):
        text = s[start:end]
        # ALWAYS a sequence key: never assume a `name` variable exists.
        key = gv + '.seq()'
        s = s[:start] + gv + ".live(" + key + ", () => " + text + ")" + s[end:]

    io.open(path, 'w', encoding='utf-8').write(s)
    return 'var=%s consts=%d live=%d' % (gv, n_consts, n_live)

if __name__ == '__main__':
    for p in sys.argv[1:]:
        print('%-38s %s' % (os.path.basename(p), convert(p)))
