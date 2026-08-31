'use strict';
/**
 * What a browser gets WHEN IT ATTACHES TO A SESSION THAT IS ALREADY RUNNING.
 *
 * ---- THE QUESTION NO OTHER CHECK ASKS -------------------------------------
 *
 * `tools/initial-state-audit.js` reads the source and asks whether an event is
 * sent on the handshake. This runs the server and asks whether it ARRIVES, on a
 * session whose collectors have already ticked — which is the case the defects
 * of 2026-08-28 all lived in. Socket A opens the session and holds it; socket B
 * attaches later and can only receive these events from a REPLAY.
 *
 * ---- IT WAS CHECKED AGAINST A BINARY WITH THE FIX REMOVED -----------------
 *
 * A probe that passes either way proves nothing. Built with the seven replays
 * disabled, socket B saw:
 *
 *   MISS lan:wan            never arrived
 *   MISS netwatch:update    never arrived
 *   OK   system:update      295ms      OK   talkers:update  524ms
 *   OK   ifstatus:names     600ms      OK   wan:status      942ms
 *   OK   ping:update       1391ms
 *
 * So it discriminates — and the result is more interesting than a clean split.
 * TWO of the seven were genuinely unreachable without the replay, both
 * slow-polling collectors. The other five arrived anyway, because their
 * collectors tick within a second or so.
 *
 * That is still a fix worth having: with the replays every one of the seven
 * arrives in ABOUT 50ms, at attach, deterministically. "Usually shows up inside
 * a second and a half if the collector happens to tick" is not the contract the
 * live app offers, and it is not one a page can be written against.
 *
 * ---- USAGE ----------------------------------------------------------------
 *
 *   MDU=<user> MDP=<password> WS_PATH=<path to ws> node tools/attach-probe.js [settle] [window]
 *
 * Hand-run, like `tools/live-diff.sh`: it needs a running Go server on :3097,
 * the live /data, and a dashboard login. Credentials come from the ENVIRONMENT
 * and are never written anywhere.
 */
// `ws` from the app container's node_modules, because this repo has no node
// dependencies of its own — the same arrangement `tools/live-socket-diff.js`
// uses, and WS_PATH points at a copy.
//
// NOT the global `WebSocket`. Node has had one built in since 22, so omitting
// this line does not fail loudly: `new WebSocket(...)` succeeds and returns an
// object with `addEventListener` and no `.on`, and every listener this file
// registers is silently never called. That is exactly what happened when this
// tool was promoted out of a scratch file — the promotion cut the requires, and
// it was not re-run in its final form until the next session tried to use it.
const WebSocket = require(process.env.WS_PATH || '/app/node_modules/ws');
const http = require('node:http');

if (typeof WebSocket !== 'function' || !WebSocket.prototype || !WebSocket.prototype.on) {
  throw new Error('WS_PATH did not resolve to the `ws` package: the object it gave back has no '
    + '`.on`, which means every listener below would be registered and never fire');
}
const WANT = ['ifstatus:names','system:update','wan:status','lan:wan',
              'netwatch:update','talkers:update','ping:update'];
const SETTLE = Number(process.argv[2] || 25), WINDOW = Number(process.argv[3] || 5);
const body = JSON.stringify({username: process.env.MDU, password: process.env.MDP});
const req = http.request({host:'127.0.0.1',port:3097,path:'/api/auth/login',method:'POST',
  headers:{'Content-Type':'application/json','Content-Length':Buffer.byteLength(body)}}, (res)=>{
  const jar=(res.headers['set-cookie']||[]).map(c=>c.split(';')[0]).join('; ');
  res.resume();
  http.get({host:'127.0.0.1',port:3097,path:'/api/routers',headers:{Cookie:jar}},(r2)=>{
    let b=''; r2.on('data',d=>b+=d); r2.on('end',()=>{
      const rid=JSON.parse(b).routers[0].id;
      const open=(onMsg)=>{const w=new WebSocket('ws://127.0.0.1:3097/ws',{headers:{Cookie:jar}});
        w.on('open',()=>{w.send(JSON.stringify({event:'router:select',data:rid}));
          w.send(JSON.stringify({event:'page:focus',data:'dashboard'}));});
        if(onMsg)w.on('message',onMsg); return w;};
      const a=open(null);
      console.log(`socket A holding the session for ${SETTLE}s so the collectors settle...`);
      setTimeout(()=>{
        const seen=new Map(); const t0=Date.now();
        open((raw)=>{try{const m=JSON.parse(raw.toString());
          if(m.event&&!seen.has(m.event))seen.set(m.event,Date.now()-t0);}catch{}});
        setTimeout(()=>{
          console.log(`\nsocket B, ${WINDOW}s after attaching to the RUNNING session:`);
          let miss=0;
          for(const e of WANT){const at=seen.get(e);
            console.log(`  ${at!==undefined?'OK  ':'MISS'} ${e.padEnd(18)} ${at!==undefined?at+'ms':'never arrived'}`);
            if(at===undefined)miss++;}
          console.log(`\n  ${WANT.length-miss}/${WANT.length} arrived; ${seen.size} events total`);
          try{a.close();}catch{}
          process.exit(miss?1:0);
        },WINDOW*1000);
      },SETTLE*1000);
    });
  });
});
req.end(body);
