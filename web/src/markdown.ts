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
 * wrong — a model that emits `<img onerror=…>` renders those characters, because
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
 * reading — a DHCP host name is chosen by the device, not by the operator. An
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
 * characters this would otherwise read as emphasis — `/ip firewall filter add
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

/** `**bold**`, `*italic*`, `_italic_` and `` `code` ``, as nodes. */
const INLINE = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|_[^_]+_)/;

/**
 * Render one line of inline markup into a fragment.
 *
 * EVERY BRANCH ENDS IN textContent. There is no path from model output to markup
 * here, which is what makes the whole module safe rather than carefully escaped
 * — escaping is something one can forget in a single branch.
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
      code.textContent = b.text;
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
