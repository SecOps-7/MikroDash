// A stand-in for the Node endpoints a local verification run cannot
// authenticate against.
//
// WHY THIS IS IN THE REPO. Verifying a ported page means driving the Go server
// in a browser, and the Go server delegates sessions to Node — see
// internal/server/auth.go. Getting a real session means a real password, which
// a verification run has no business holding. This answers /api/auth/status with
// a synthetic session, /api/routers from the router list and /api/localcc from
// --localcc, and PROXIES everything else to the real Node app so the page still
// gets the real stylesheet, fonts and logo.
//
// NEVER PART OF THE APP, and it cannot become one: nothing in cmd/ or internal/
// references it, it listens only on 127.0.0.1, and the Go server has to be
// pointed at it explicitly with -node. It grants exactly the pages named on the
// command line, so a verification run exercises the same permission gate a real
// session would.
//
//   node tools/dev-stub-auth.mjs --routers <id,id> --pages dns=write,bridges=read \
//     [--user <real-username>] [--localcc US]
//   ...then run the server with -node http://127.0.0.1:3099
import http from 'node:http';

const arg = (name, dflt) => {
  const i = process.argv.indexOf('--' + name);
  return i > -1 && process.argv[i + 1] ? process.argv[i + 1] : dflt;
};

const RIDS = arg('routers', '').split(',').filter(Boolean);
const PAGES = Object.fromEntries(arg('pages', 'dns=write').split(',').filter(Boolean)
  .map(p => { const [k, v] = p.split('='); return [k, v || 'read']; }));
const ROUTERS = JSON.parse(arg('routerList', '[]'));
const UPSTREAM = Number(arg('upstream', '3081'));
const PORT = Number(arg('port', '3099'));
// WHO THE SESSION CLAIMS TO BE, AND WHY IT MATTERS NOW. The Go server resolves
// this username against users.json and then asks the real grant graph whether
// that principal may see the page (internal/rbac). A name nobody holds resolves
// to no user id and every page is refused — correctly. So a verification run
// has to impersonate a REAL principal, and `--user` is how. It still grants only
// the pages named on the command line: the union gate and the graph are ANDed,
// so this can never widen access beyond what the graph already allows.
const USER = arg('user', 'harness');
// The country the connections map draws its arcs FROM. Empty means "answer 401",
// which is what an unauthenticated proxy pass does.
const LOCALCC = arg('localcc', 'US');

http.createServer((req, res) => {
  if (req.url.startsWith('/api/auth/status')) {
    res.setHeader('content-type', 'application/json');
    return res.end(JSON.stringify({
      authMode: 'modern', firstRun: false,
      session: { username: USER, role: 'admin',
                 caps: { pages: PAGES, routers: { readable: RIDS } } },
    }));
  }
  if (req.url.startsWith('/api/routers')) {
    res.setHeader('content-type', 'application/json');
    return res.end(JSON.stringify({ ok: true, routers: ROUTERS }));
  }
  // The THIRD endpoint a verification run cannot authenticate against, and the
  // one whose absence is easiest to misread. It answers "which country is this
  // router in", which the connections map uses as the ORIGIN of every arc. Left
  // proxied it returns 401, the page keeps `localCC = 'ZZ'`, and the map draws
  // no arcs at all — a page that looks like a geo defect and is a session
  // problem. `--localcc ''` restores the 401 for anyone who wants to see that
  // degraded state deliberately.
  if (req.url.startsWith('/api/localcc')) {
    if (!LOCALCC) { res.statusCode = 401; return res.end('{}'); }
    res.setHeader('content-type', 'application/json');
    return res.end(JSON.stringify({ cc: LOCALCC }));
  }
  const up = http.request(
    { host: '127.0.0.1', port: UPSTREAM, path: req.url, method: req.method,
      headers: { ...req.headers, host: '127.0.0.1:' + UPSTREAM } },
    (r) => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
  up.on('error', () => { res.statusCode = 502; res.end('upstream down'); });
  req.pipe(up);
}).listen(PORT, '127.0.0.1', () => {
  console.error(`dev stub on 127.0.0.1:${PORT} — pages ${JSON.stringify(PAGES)}, ` +
                `${RIDS.length} router(s), everything else proxied to :${UPSTREAM}`);
});
