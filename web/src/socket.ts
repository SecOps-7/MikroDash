// A plain WebSocket wearing the surface the pages already expect.
//
// The renderers were written against `socket.on(event, cb)` and
// `socket.emit(event, data)`, and keeping that surface is what lets them be
// ported almost verbatim instead of rewritten against a different API — which
// matters because the rewrite is the risk, not the transport.
//
// It fits in a page because the app used so little of Socket.IO: named events
// in both directions, and nothing else. No acknowledgement callbacks, no
// namespaces, no binary frames. What Socket.IO was actually providing here was
// reconnection, and that is the part below with any substance to it.

type Handler = (data: any) => void;

export class Socket {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Handler[]>();
  /** Queued while the socket is down, so a page:focus during a reconnect is not lost. */
  private pending: string[] = [];
  private attempt = 0;
  private closed = false;
  private timer: number | undefined;

  constructor(private url: string) {
    this.connect();
    // A tab that was in the background for an hour comes back with a socket the
    // browser quietly killed. Reconnecting on visibility beats waiting for the
    // next backoff tick to notice.
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden && this.ws?.readyState !== WebSocket.OPEN) {
        // Returning to a backgrounded tab should resolve immediately rather
        // than waiting for the next backoff retry, which is the case the user
        // actually notices.
        this.fire('connect_error', null);
        this.connect();
      }
    });
  }

  /** Whether the socket is up right now. */
  isOpen(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  on(event: string, cb: Handler): void {
    const list = this.handlers.get(event);
    if (list) list.push(cb);
    else this.handlers.set(event, [cb]);
  }

  emit(event: string, data?: unknown): void {
    const frame = JSON.stringify({ event, data: data === undefined ? null : data });
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(frame);
    else if (this.pending.length < 64) this.pending.push(frame);
  }

  close(): void {
    this.closed = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.ws?.close();
  }

  private fire(event: string, data: unknown): void {
    // The last payload of each event, kept for the DOM-equality gate.
    //
    // PLAN.md makes "renders identically to the Node page" the acceptance
    // criterion, and the only exact way to check it is to drive BOTH renderers
    // from ONE payload and compare innerHTML. Without this the comparison has to
    // fetch its own payload, and any live field — dns cacheUsed moves every
    // tick — makes the two disagree for reasons that have nothing to do with
    // the port. It is a read-only record of what already arrived, so it
    // discloses nothing the page is not already showing.
    (window as unknown as { __lastEvent?: Record<string, unknown> }).__lastEvent ??= {};
    (window as unknown as { __lastEvent: Record<string, unknown> }).__lastEvent[event] = data;

    // …and the other half of the same affordance: a way to REPLAY an event into
    // the handlers without a router sending it.
    //
    // Needed because a page can render differently depending on an event the
    // comparison cannot provoke. The Packages page hides every action button
    // until `packages:caps` says the session may write, so a comparison that can
    // only deliver `packages:update` compares the state with no buttons in it —
    // and the buttons are the part worth comparing. This delivers exactly what
    // the socket itself would deliver, to the same handlers, so nothing is
    // simulated except the arrival.
    //
    // Read-only in the sense that matters: it dispatches INBOUND events to
    // local renderers and sends nothing to the server, so it cannot cause a
    // write. What a renderer then does with a button is still gated server-side.
    (window as unknown as { __testEmit?: (e: string, d: unknown) => void }).__testEmit ??=
      (e: string, d: unknown): void => { this.fire(e, d); };

    for (const cb of this.handlers.get(event) || []) {
      try {
        cb(data);
      } catch (e) {
        // One page's renderer throwing must not stop the others from being
        // told: a single bad payload should cost one card, not the session.
        console.error('[socket] handler for ' + event + ' threw', e);
      }
    }
  }

  private connect(): void {
    if (this.closed || this.ws?.readyState === WebSocket.CONNECTING) return;
    const ws = new WebSocket(this.url);
    this.ws = ws;

    let opened = false;

    ws.onopen = () => {
      opened = true;
      this.attempt = 0;
      this.fire('connect', null);
      const queued = this.pending;
      this.pending = [];
      for (const f of queued) ws.send(f);
    };

    ws.onmessage = (ev) => {
      let msg: { event?: string; data?: unknown };
      try {
        msg = JSON.parse(ev.data as string);
      } catch {
        return; // a frame we cannot parse is not a reason to tear anything down
      }
      if (msg.event) this.fire(msg.event, msg.data);
    };

    ws.onclose = () => {
      this.fire('disconnect', null);
      // A handshake that NEVER OPENED is a different event from a connection
      // that dropped, and only the first can mean the session is gone. The
      // server auth-gates the upgrade, so once a session dies every attempt is
      // refused and `open` never fires — no `connect`, no `session:expired`
      // (which needs a live socket), and nothing the fetch guard can see,
      // because a WebSocket handshake is not a fetch.
      //
      // Without this the tab retries forever behind a capped backoff and sits
      // on an empty dashboard until somebody reloads by hand. That is what the
      // operator hit on 2026-08-28 after a container restart.
      if (!opened) this.fire('connect_error', null);
      if (this.closed) return;
      // Capped exponential backoff. The cap matters more than the curve: this
      // is a LAN dashboard, and a client that has backed off to minutes stays
      // blank long after the server came back.
      const wait = Math.min(10000, 500 * 2 ** Math.min(this.attempt++, 5));
      this.timer = setTimeout(() => this.connect(), wait) as unknown as number;
    };

    ws.onerror = () => ws.close();
  }
}
