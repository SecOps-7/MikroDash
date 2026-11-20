// The share shell's fetch: what a share view may send, decided in one place
// so web/test/share-fetch.test.ts can hold it (the operator's ruling of
// 2026-11-20: a share view never uses a signed-in session in the same
// browser).
//
// - Every request goes out WITHOUT credentials, so a browser that is also
//   signed in to MikroDash lends this page nothing of that account.
// - Nothing but GET leaves the page, apart from the ticket renewal.
// - An /api/ read the link allows is sent to /share/api/ with the view
//   ticket; any other /api/ call is answered here, without a request, as
//   refused. A refused share read is retried once, briefly later: a page can
//   ask the moment it is shown, a moment before the server has moved the
//   socket to it.

type Fetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

function refused(): Promise<Response> {
  return Promise.resolve(new Response(JSON.stringify({ ok: false, error: 'not available in a shared view' }),
    { status: 403, headers: { 'Content-Type': 'application/json' } }));
}

/** The fetch a share view uses, wrapping the browser's own. */
export function shareFetch(real: Fetch, reads: readonly string[], ticket: () => string, origin: string): Fetch {
  return (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const method = (init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, origin);
    const renewal = url.pathname === '/share/api/ticket' && method === 'POST';
    if (method !== 'GET' && !renewal) return refused();
    if (url.origin === origin && url.pathname.startsWith('/api/')) {
      if (!reads.includes(url.pathname)) return refused();
      const to = '/share' + url.pathname + url.search;
      const send = (): Promise<Response> => real(to, {
        method: 'GET', credentials: 'omit', headers: { Authorization: 'Share ' + ticket() },
      });
      return send().then((r) => (r.status === 404
        ? new Promise<Response>((res) => setTimeout(() => res(send()), 400)) : r));
    }
    // Headers pass through: the renewal carries its ticket that way.
    return real(input, { ...init, credentials: 'omit' });
  };
}
