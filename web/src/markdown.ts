/**
 * A deliberately small Markdown subset, rendered as DOM nodes.
 *
 * ── WHY NOT A PARSER ────────────────────────────────────────────────────────
 *
 * Issue #98 called Markdown rendering "a dependency trap", and it is right about
 * the usual route: a parser produces an HTML string, which needs a sanitiser,
 * which is two runtime dependencies for a project whose `web/package.json`
 * carries esbuild and typescript and nothing else.
 *
 * The trap is the HTML string, not the Markdown. Nothing here ever produces one.
 * Every piece of model output reaches the page through `createElement` and
 * `textContent`, so there is no markup to sanitise and no `innerHTML` to get
 * wrong - a model that emits `<img onerror=…>` renders those characters, because
 * that is what they are.
 *
 * ── WHAT IS SUPPORTED, AND WHY THIS MUCH ────────────────────────────────────
 *
 * Fenced code, inline code, tables, ordered and unordered lists, headings, bold
 * and italic, and links AS TEXT. That is what an assistant answering questions
 * about a router actually emits: a RouterOS command belongs in a code block, and
 * a comparison of interfaces belongs in a table.
 *
 * ── LINKS ARE NOT CLICKABLE, AND THAT IS THE POINT ──────────────────────────
 *
 * A model can be talked into emitting a link by the very router text it is
 * reading - a DHCP host name is chosen by the device, not by the operator. An
 * anchor whose href came from that chain is a phishing target rendered by the
 * operator's own dashboard, so the URL is shown as text and the reader decides.
 */

/** One block of the document, after splitting. */
type Block =
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'heading'; level: number; text: string }
  | { kind: 'list'; ordered: boolean; items: string[] }
  | { kind: 'table'; header: string[]; rows: string[][] }
  | { kind: 'para'; text: string };

const FENCE = /^```(\S*)\s*$/;
const HEADING = /^(#{1,6})\s+(.*)$/;
const UL = /^[-*]\s+(.*)$/;
const OL = /^\d+[.)]\s+(.*)$/;
/** A table row: at least one pipe at each end. */
const ROW = /^\s*\|(.+)\|\s*$/;
/** The separator under a table header: pipes, dashes and colons only. */
const SEP = /^\s*\|[\s:|-]+\|\s*$/;

function cells(line: string): string[] {
  const m = ROW.exec(line);
  if (!m) return [];
  return m[1]!.split('|').map((c) => c.trim());
}

/**
 * Split the text into blocks.
 *
 * FENCED CODE IS TAKEN FIRST AND VERBATIM. Everything between the fences is one
 * string with no inline parsing at all, because a RouterOS command is full of
 * characters this would otherwise read as emphasis - `/ip firewall filter add
 * chain=input action=drop` survives only if nothing looks at it.
 *
 * AN UNCLOSED FENCE IS STILL A CODE BLOCK. A truncated reply ends mid-command,
 * and rendering the remainder as prose would reflow it into something that looks
 * like a sentence and is not.
 */
export function parseBlocks(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, '\n').split('\n');
  const out: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i]!;

    const fence = FENCE.exec(line);
    if (fence) {
      const lang = fence[1] || '';
      const body: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(lines[i]!)) body.push(lines[i++]!);
      if (i < lines.length) i++; // the closing fence
      out.push({ kind: 'code', lang, text: body.join('\n') });
      continue;
    }

    if (!line.trim()) { i++; continue; }

    const heading = HEADING.exec(line);
    if (heading) {
      out.push({ kind: 'heading', level: heading[1]!.length, text: heading[2]! });
      i++;
      continue;
    }

    // A table needs a header row AND the separator under it; a lone line with
    // pipes in it is prose that happens to contain pipes.
    if (ROW.test(line) && i + 1 < lines.length && SEP.test(lines[i + 1]!)) {
      const header = cells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && ROW.test(lines[i]!)) rows.push(cells(lines[i++]!));
      out.push({ kind: 'table', header, rows });
      continue;
    }

    if (UL.test(line) || OL.test(line)) {
      const ordered = OL.test(line);
      const items: string[] = [];
      while (i < lines.length) {
        const m = ordered ? OL.exec(lines[i]!) : UL.exec(lines[i]!);
        if (!m) break;
        items.push(m[1]!);
        i++;
      }
      out.push({ kind: 'list', ordered, items });
      continue;
    }

    // A paragraph runs until a blank line or the start of another block.
    const para: string[] = [];
    while (i < lines.length && lines[i]!.trim()
      && !FENCE.test(lines[i]!) && !HEADING.test(lines[i]!)
      && !UL.test(lines[i]!) && !OL.test(lines[i]!)
      && !ROW.test(lines[i]!)) {
      para.push(lines[i++]!);
    }
    if (para.length) out.push({ kind: 'para', text: para.join(' ') });
    else i++; // a line that starts a block already handled; never loop
  }
  return out;
}


/**
 * RouterOS commands, coloured by part.
 *
 * ── A STRICT PARTITION, WHICH IS THE WHOLE CONTRACT ─────────────────────────
 *
 * Every character of the input lands in exactly one token, in order, and nothing
 * is inserted. So joining the tokens reproduces the source byte for byte, and
 * the rendered `<code>` still has the command as its `textContent` -- which is
 * what makes a highlighted command still copyable, and what lets the existing
 * "survives verbatim" test keep passing rather than being re-aimed.
 *
 * Anything unrecognised falls through as plain text rather than being dropped.
 * A highlighter that loses what it cannot classify is worse than none: the
 * operator copies a command with a piece missing.
 */
export type RosToken = { cls: string; text: string };

/** Languages a model actually labels RouterOS blocks with. */
const ROS_LANGS = new Set(['routeros', 'ros', 'mikrotik', 'rsc', 'winbox', 'terminal']);

/**
 * Is this block RouterOS?
 *
 * A NAMED LANGUAGE IS BELIEVED, INCLUDING WHEN IT SAYS SOMETHING ELSE. A block
 * marked `json` is not run through a RouterOS highlighter just because it starts
 * with a slash. Only an UNLABELLED block is sniffed, and then only by the one
 * signal that is reliable: a RouterOS command begins at a menu path.
 */
export function looksLikeRouterOS(text: string, lang: string): boolean {
  if (lang) return ROS_LANGS.has(lang.toLowerCase());
  // ── COMMENTS AND BLANKS ARE SKIPPED WHEN SNIFFING ───────────────────────
  //
  // A model habitually explains before it commands, so the first line of an
  // unlabelled block is often `# add a static entry` rather than the command.
  // Judging that line alone left every commented example uncoloured, which is
  // most of them.
  for (const line of text.split('\n')) {
    const t = line.trim();
    if (!t || t.startsWith('#')) continue;
    return t.startsWith('/');
  }
  return false;
}

/** Matched at the start of what is left, in this order. Order is meaning. */
const ROS_RULES: { cls: string; re: RegExp }[] = [
  { cls: 'ros-comment', re: /^#[^\n]*/ },
  { cls: 'ros-string', re: /^"(?:[^"\\]|\\.)*"/ },
  // A menu path: /ip, /ip/dns/static. The space-separated CLI form leaves its
  // later words plain, deliberately -- consuming them risks swallowing the verb,
  // and a wrong colour reads worse than none.
  { cls: 'ros-path', re: /^\/[A-Za-z][\w-]*(?:\/[A-Za-z][\w-]*)*/ },
  { cls: 'ros-verb', re: /^(?:add|set|remove|print|enable|disable|move|export|import|find|monitor|unset|ensure)\b/ },
  { cls: 'ros-key', re: /^[A-Za-z][\w-]*(?==)/ },
  { cls: 'ros-op', re: /^=/ },
  { cls: 'ros-num', re: /^\d+(?:\.\d+)*(?:\/\d+)?(?::\d+)?/ },
];

export function tokenizeRouterOS(src: string): RosToken[] {
  const out: RosToken[] = [];
  const push = (cls: string, text: string): void => {
    if (!text) return;
    const last = out[out.length - 1];
    // Merge runs of plain text so the DOM does not gain a node per character.
    if (last && last.cls === '' && cls === '') last.text += text;
    else out.push({ cls, text });
  };

  let i = 0;
  let afterOp = false;
  while (i < src.length) {
    const rest = src.slice(i);

    // A VALUE IS POSITIONAL, not lexical: it is whatever follows an `=`. Without
    // this, `address=10.0.0.1` colours the number and leaves `server.lan` looking
    // like prose.
    if (afterOp) {
      // A QUOTED VALUE IS A STRING FIRST. `comment="on the roof"` is the common
      // shape, and without this the value branch swallows the quotes and the
      // string colour is unreachable in practice.
      const str = /^"(?:[^"\\]|\\.)*"/.exec(rest);
      if (str) { push('ros-string', str[0]); i += str[0].length; afterOp = false; continue; }
      const key = /^[A-Za-z][\w-]*(?==)/.exec(rest);
      if (key) { push('ros-key', key[0]); i += key[0].length; afterOp = false; continue; }
      const val = /^[^\s=]+/.exec(rest);
      if (val) {
        // AN ADDRESS IS NOT A WORD. Numbers, IPs, prefixes and ports get their
        // own colour, so `address=10.0.0.1/24` reads differently from
        // `name=server.lan` at a glance.
        const numeric = /^\d[\d.:/]*$/.test(val[0]);
        push(numeric ? 'ros-num' : 'ros-value', val[0]);
        i += val[0].length;
        afterOp = false;
        continue;
      }
      afterOp = false;
    }

    let hit = false;
    for (const rule of ROS_RULES) {
      const m = rule.re.exec(rest);
      if (m && m[0]) {
        push(rule.cls, m[0]);
        i += m[0].length;
        afterOp = rule.cls === 'ros-op';
        hit = true;
        break;
      }
    }
    if (!hit) { push('', src[i]!); i += 1; }
  }
  return out;
}

/** The tokens as nodes. Every branch ends in textContent, as everywhere here. */
function rosNodes(src: string): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const t of tokenizeRouterOS(src)) {
    if (!t.cls) { frag.appendChild(document.createTextNode(t.text)); continue; }
    const span = document.createElement('span');
    span.className = t.cls;
    span.textContent = t.text;
    frag.appendChild(span);
  }
  return frag;
}

/** `**bold**`, `*italic*`, `_italic_` and `` `code` ``, as nodes. */
const INLINE = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|_[^_]+_)/;

/**
 * Render one line of inline markup into a fragment.
 *
 * EVERY BRANCH ENDS IN textContent. There is no path from model output to markup
 * here, which is what makes the whole module safe rather than carefully escaped
 * - escaping is something one can forget in a single branch.
 */
export function inline(text: string): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const part of text.split(INLINE)) {
    if (!part) continue;
    if (part.length > 2 && part.startsWith('`') && part.endsWith('`')) {
      const code = document.createElement('code');
      code.textContent = part.slice(1, -1);
      code.className = 'md-code';
      frag.appendChild(code);
    } else if (part.length > 4 && part.startsWith('**') && part.endsWith('**')) {
      const b = document.createElement('strong');
      b.textContent = part.slice(2, -2);
      frag.appendChild(b);
    } else if (part.length > 2 && ((part.startsWith('*') && part.endsWith('*'))
      || (part.startsWith('_') && part.endsWith('_')))) {
      const em = document.createElement('em');
      em.textContent = part.slice(1, -1);
      frag.appendChild(em);
    } else {
      frag.appendChild(document.createTextNode(part));
    }
  }
  return frag;
}

/**
 * Render Markdown into a fresh element.
 *
 * The caller appends the result. Nothing is ever assigned to `innerHTML`, here
 * or in the page module that uses this.
 */
export function renderMarkdown(src: string): HTMLElement {
  const root = document.createElement('div');
  root.className = 'md';

  for (const b of parseBlocks(src)) {
    if (b.kind === 'code') {
      const pre = document.createElement('pre');
      pre.className = 'md-pre';
      const code = document.createElement('code');
      // VERBATIM, and the language is a data attribute rather than part of a
      // class name: a class built from model output would let it choose a
      // selector the stylesheet defines.
      //
      // HIGHLIGHTING DOES NOT CHANGE THE TEXT. `tokenizeRouterOS` partitions the
      // source, so `code.textContent` is still the command exactly as the model
      // wrote it, and the operator still copies something that runs.
      if (looksLikeRouterOS(b.text, b.lang)) {
        code.className = 'md-ros';
        code.appendChild(rosNodes(b.text));
      } else {
        code.textContent = b.text;
      }
      if (b.lang) code.setAttribute('data-lang', b.lang);
      pre.appendChild(code);
      root.appendChild(pre);
      continue;
    }
    if (b.kind === 'heading') {
      // FLOORED AT h4. These sit inside a chat bubble, and an h1 from a model
      // would out-shout the page's own headings.
      const h = document.createElement('h' + Math.min(6, Math.max(4, b.level + 3)));
      h.appendChild(inline(b.text));
      h.className = 'md-h';
      root.appendChild(h);
      continue;
    }
    if (b.kind === 'list') {
      const list = document.createElement(b.ordered ? 'ol' : 'ul');
      list.className = 'md-list';
      for (const item of b.items) {
        const li = document.createElement('li');
        li.appendChild(inline(item));
        list.appendChild(li);
      }
      root.appendChild(list);
      continue;
    }
    if (b.kind === 'table') {
      const wrap = document.createElement('div');
      wrap.className = 'md-table-wrap';
      const table = document.createElement('table');
      table.className = 'md-table';
      const thead = document.createElement('thead');
      const hrow = document.createElement('tr');
      for (const c of b.header) {
        const th = document.createElement('th');
        th.appendChild(inline(c));
        hrow.appendChild(th);
      }
      thead.appendChild(hrow);
      table.appendChild(thead);
      const tbody = document.createElement('tbody');
      for (const row of b.rows) {
        const tr = document.createElement('tr');
        for (const c of row) {
          const td = document.createElement('td');
          td.appendChild(inline(c));
          tr.appendChild(td);
        }
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      wrap.appendChild(table);
      root.appendChild(wrap);
      continue;
    }
    const p = document.createElement('p');
    p.className = 'md-p';
    p.appendChild(inline(b.text));
    root.appendChild(p);
  }
  return root;
}
