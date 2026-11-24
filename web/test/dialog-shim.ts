// THE APP'S OWN DIALOGS, ANSWERED FROM A TEST.
//
// The app never uses the browser's confirm(), prompt() or alert(): every page
// asks through web/src/confirm.ts, which builds a `.rtr-modal-bg` node, appends
// it to document.body and resolves when one of its `data-answer` buttons is
// clicked. A test that used to stub `window.confirm` drives that dialog instead,
// through the same click the page listens for, so it exercises the real
// confirm.ts rather than a stand-in for it.
//
// `installDialogs(doc, reply)` adds to `doc` the little confirm.ts needs
// (createElement, body.appendChild, add/removeEventListener) without replacing
// what the document already has, and answers every dialog that opens:
//
//   reply returns true / false  -> the confirm button / Cancel
//   reply returns a string      -> askText's OK with that text in the field
//   reply returns null          -> askText's Cancel
//
// The answer is given on a microtask, after the page has finished opening the
// dialog, as a person's click would be: a test awaits before it asserts.
//
// Every dialog is recorded in `asked`, with its title and its visible text
// (tags stripped, entities decoded), so a test can still check what the
// operator was asked.

export interface Asked { title: string; text: string }

const decode = (s: string): string => s.replace(/<[^>]*>/g, ' ')
  .replace(/&quot;/g, '"').replace(/&#39;/g, '\'').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
  .replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim();

export function installDialogs(doc, reply: (d: Asked) => boolean | string | null): { asked: Asked[] } {
  const asked: Asked[] = [];
  const make = doc.createElement;
  doc.createElement = (tag) => {
    const n = make ? make.call(doc, tag) : {};
    const clicks = [];
    const add = n.addEventListener;
    n.addEventListener = (ev, fn, ...rest) => {
      if (ev === 'click') clicks.push(fn);
      if (add) add.call(n, ev, fn, ...rest);
    };
    n.__dialogClicks = clicks;
    if (!n.remove) n.remove = () => {};
    if (!n.querySelector) n.querySelector = () => null;
    return n;
  };
  if (!doc.addEventListener) doc.addEventListener = () => {};
  if (!doc.removeEventListener) doc.removeEventListener = () => {};
  const body = doc.body || (doc.body = {});
  const append = body.appendChild;
  body.appendChild = (n) => {
    if (append) append.call(body, n);
    if (!/\brtr-modal-bg\b/.test(String(n && n.className))) return n;
    const html = String(n.innerHTML || '');
    const title = decode((/class="rtr-modal-title">([^<]*)</.exec(html) || ['', ''])[1]);
    const d = { title, text: decode(html.replace(/<button[\s\S]*?<\/button>/g, ' ')) };
    asked.push(d);
    queueMicrotask(() => {
      const v = reply(d);
      const yes = v === true || typeof v === 'string';
      const query = n.querySelector;
      n.querySelector = (sel) => (sel === '#confirmAskValue'
        ? { value: typeof v === 'string' ? v : '' } : query.call(n, sel));
      for (const fn of n.__dialogClicks.slice()) {
        fn({ target: { closest: () => ({ dataset: { answer: yes ? 'yes' : 'no' } }) },
          preventDefault() {}, stopPropagation() {} });
      }
    });
    return n;
  };
  return { asked };
}
