import io, re, sys, os

def match_paren(s, i):
    depth, n, instr, q = 0, len(s), False, ''
    while i < n:
        c = s[i]
        if instr:
            if c == '\\': i += 2; continue
            if c == q: instr = False
        elif c in '"\'`': instr, q = True, c
        elif c == '(': depth += 1
        elif c == ')':
            depth -= 1
            if depth == 0: return i + 1
        i += 1
    raise ValueError('unbalanced')

def split_args(s):
    """Top-level comma split, respecting nesting and strings."""
    out, depth, cur, instr, q = [], 0, [], False, ''
    for c in s:
        if instr:
            cur.append(c)
            if c == q: instr = False
            continue
        if c in '"\'`': instr, q = True, c
        elif c in '([{': depth += 1
        elif c in ')]}': depth -= 1
        elif c == ',' and depth == 0:
            out.append(''.join(cur)); cur = []; continue
        cur.append(c)
    out.append(''.join(cur))
    return [a.strip() for a in out]

def wrap(path, fn='cmp', gv='G'):
    s = io.open(path, encoding='utf-8').read()
    spans = []
    for m in re.finditer(r'(?<![\w.])' + fn + r'\(', s):
        # skip the definition
        line_start = s.rfind('\n', 0, m.start()) + 1
        if s[line_start:m.start()].strip().startswith('function'): continue
        end = match_paren(s, m.end() - 1)
        inner = s[m.end():end - 1]
        args = split_args(inner)
        if len(args) != 3: continue
        if args[1].startswith(gv + '.live('): continue
        spans.append((m.start(), end, args))
    n = 0
    for start, end, args in reversed(spans):
        # PARENTHESISE THE BODY. An argument that is an object literal —
        # `cmp(k, { colW: x }, y)` — becomes `() => { colW: x }`, which parses as
        # an arrow with a BLOCK body and a label, not as a returned object. Found
        # by grid-layout-check, which has one.
        new = '%s(%s, %s.live(%s, () => (%s)), %s)' % (fn, args[0], gv, args[0], args[1], args[2])
        s = s[:start] + new + s[end:]
        n += 1
    io.open(path, 'w', encoding='utf-8').write(s)
    return n

if __name__ == '__main__':
    fn = sys.argv[2] if len(sys.argv) > 2 else 'cmp'
    gv = sys.argv[3] if len(sys.argv) > 3 else 'G'
    print('%s: wrapped %d %s() call(s)' % (os.path.basename(sys.argv[1]), wrap(sys.argv[1], fn, gv), fn))
