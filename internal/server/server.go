package server

// The strangler boundary.
//
// Go sits in front and answers only what it has actually ported; everything
// else is proxied to the Node app untouched. That includes `/socket.io/*`, so
// every unported page keeps working exactly as it does today — which is the
// whole point. Socket.IO is not reimplemented, it is passed through, and it is
// deleted when the last page moves rather than before.
//
// The new frontend is mounted under a PREFIX rather than at `/`. During the
// port both implementations must be reachable in one browser with one session,
// so that a ported page can be compared against the original side by side
// instead of replacing it and hoping. Cookies ignore port numbers, so a login
// taken on the Node port is already valid here.

import (
	"log"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/alertwire"
	"mikrodash/internal/geo"
	"mikrodash/internal/websession"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/changelog"
	"mikrodash/internal/connstate"
	"mikrodash/internal/db"
	"mikrodash/internal/historywire"
	"mikrodash/internal/hub"
	"mikrodash/internal/pages"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routers"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
	"mikrodash/internal/wifiscan"
)

// Options configures the server.
type Options struct {
	// GeoDir is where geoip-lite keeps its data, for the location picker's
	// gazetteer. Empty means the picker reports itself unavailable, which is a
	// supported state rather than an error.
	GeoDir string
	// NoPool turns OFF the background pool and the fleet holds: the connection
	// to every router nobody has open, which answers the Devices page and keeps
	// alerting running. For a second process pointed at a fleet another
	// MikroDash already polls, where two pools would double the channels held
	// on the same hardware (API channels are the documented bottleneck).
	NoPool bool
	// AlertDispatch turns alert NOTIFICATIONS on; the evaluator and its database
	// rows run either way. Off unless passed (the image passes it), because a
	// second process watching the same routers would send every alert twice,
	// and a message cannot be unsent.
	AlertDispatch bool
	// BackupScheduler turns SCHEDULED backups on. Off unless passed (the image
	// passes it): two schedulers would take two backups per router per schedule.
	BackupScheduler bool

	// Retention turns the daily database sweep on. Off unless passed (the image
	// passes it): it DELETES, and must be asked for.
	Retention bool
	// History turns the traffic/ping/connectivity RECORDING on. Off unless passed
	// (the image passes it): two processes bucketing the same samples double
	// every minute row, and Reports averages by minute.
	History bool
	// StaticDir is the shared asset tree — `/vendor/*`, `/css/*`, `/logo.png`,
	// `/preflight.js`, and the login page.
	//
	// ── WHY IT IS NEEDED, AND ONLY AT CUTOVER ───────────────────────────
	//
	// `web/build.mjs` USED TO SAY: "the external stylesheets are NOT copied: the
	// Node app still serves them and the Go server proxies them, so both
	// implementations share one copy rather than drifting apart". Right while
	// Node runs, and fatal when it stops — the ported SPA references EIGHT
	// assets nobody would then serve, so it rendered unstyled with every chart
	// dead. Found by running standalone against the live /data and watching
	// /vendor/tabler.min.css answer 502.
	//
	// **THAT QUOTE WAS SUPERSEDED ON 2026-08-27 and this comment carried it
	// until 2026-08-28.** The assets are now VENDORED into `web/public/`
	// (117 files: `vendor/tabler.min.css`, `vendor/chart.umd.min.js`,
	// `vendor/fonts`, `vendor/world-atlas`, `css/`, `preflight.js`, the login
	// page and the logo), with licences in THIRD_PARTY_NOTICES.md — which had to
	// exist before any of it was committed. build.mjs says so in its own
	// corrected note. The drift the old arrangement guarded against ends at
	// cutover anyway: there is no second implementation to drift from once the
	// JS is removed.
	//
	// So this flag now points at a tree THIS repo owns rather than at Node's.
	// Empty leaves the behaviour as it was: proxied, which is correct while Node
	// is up and is what coexistence runs on today.
	StaticDir string
	// WebDir holds the built frontend.
	WebDir string
	// OriginPatterns are accepted Origin hosts for the WebSocket handshake.
	//
	// ── THIS COMMENT USED TO SAY THE OPPOSITE, AND IT COST A RELEASE ────────
	//
	// It read "Empty means same-origin only, which is what a reverse-proxied
	// deployment wants." That is backwards. A reverse proxy is the one case
	// where the browser's Origin and this process's Host CANNOT match: the
	// browser says `dash.example.com`, the proxy forwards to `10.0.0.5:3081`.
	// So empty is exactly what a proxied deployment does not want, and because
	// the comment said otherwise nothing ever wired a flag to this field —
	// leaving every published Go image unable to open a socket behind a proxy
	// (issue #128, reported against 0.8.20, true since the v0.8.0 cutover).
	//
	// Empty still MEANS same-origin only, and that is still the right default:
	// this check is what stops a hostile page opening an authenticated socket
	// to a MikroDash the victim is signed in to. It is opened deliberately,
	// with -origins / MIKRODASH_ORIGINS, and never inferred from a forwarded
	// header a client could forge.
	OriginPatterns []string

	// TrustedProxies are the peers whose X-Forwarded-For is believed, from
	// `-trusted-proxies` / MIKRODASH_TRUSTED_PROXIES. Empty trusts none. A
	// deployment fact rather than a setting, so an admin session cannot widen it.
	// See internal/trustedproxy and issue #111.
	TrustedProxies []netip.Prefix
	// AuditDB is the shared SQLite trail. Nil disables audit recording.
	AuditDB *db.DB

	// ListenAddr is what this process was told to serve on, kept so a RESTORE
	// can tell a router where to fetch from.
	//
	// It is the port, not the host: the host is discovered from the router's own
	// view of us (`/user/active/print`), because only the router knows which of
	// our addresses it can reach. See `backupsRestore`.
	ListenAddr string
}

// Server is the whole thing.
type Server struct {
	// secScans is the Security Scan page's last report per router (secscan.go).
	secScans       secScanStore
	hub            *hub.Hub
	auth           *Auth
	sessions       *session.Manager
	web            http.Handler
	originPatterns []string
	// writeLimit bounds router writes per user per router (#97). See write_limit.go.
	writeLimit *rateLimiter
	// aiLimit bounds questions to the model, per user per router (#98).
	//
	// EVERY ASK IS AN OUTBOUND REQUEST to an endpoint the operator named, which
	// is the same reason the Test button has a limiter of its own. A socket
	// frame is cheaper to send than an HTTP request, so the need is greater
	// here, not smaller.
	aiLimit *rateLimiter
	// overviewLines is the last Agent Overview line per user per router, so a
	// return to the Dashboard within the interval re-shows it rather than paying
	// for another. See `overviewTick`.
	overviewMu    sync.Mutex
	overviewLines map[string]overviewEntry
	// idleGrace is how long a page-level suspend waits after the last viewer
	// leaves a collector's rooms. Zero means session.DefaultIdleGrace; only
	// tests set it, because two minutes is not a thing a test can wait for.
	idleGrace time.Duration
	// suspendOne replaces `(*session.Session).SuspendCollector` in
	// `suspendAfterGrace`. Nil in production; only tests set it, for the reason
	// that helper records — a Session a unit test can build has no collectors
	// behind the table, so the real call panics rather than reporting anything.
	suspendOne func(rs *session.Session, key string)

	// changelog fetches RouterOS release notes for the Update dialog. One per
	// server so its cache is shared across sockets — a changelog is immutable
	// and per-connection caches would fetch the same text once per viewer.
	changelog *changelog.Client

	// pool holds a connection to every router NOBODY has open, running the three
	// collectors the Devices page's rows need. Nil until a caller builds one.
	//
	// SUSPENDED WHENEVER NOBODY IS ON THE PAGE, which is what keeps it cheap: the
	// rows are only wanted while somebody is looking at them.
	pool *routers.Pool
	// alerts evaluates collector payloads into alert rows. Nil without a history
	// database, and nil is inert. It DOES NOT DISPATCH — see alert_wire.go.
	alerts *alertwire.Wire
	// dispatch sends alert notifications. OFF unless `-alert-dispatch` is given.
	// Built even when off, so the switch is one boolean rather than a nil check
	// scattered through the caller.
	dispatch *alertdispatch.Dispatcher
	// backupSched takes scheduled backups. Nil unless `-backup-scheduler`.
	// STARTED BY NOBODY even when built: `Scheduler.Start` is the cutover step.
	backupSched *backups.Scheduler
	// reportSched sends due report schedules. Nil unless `-alert-dispatch`.
	reportSched *reportScheduler
	// The daily retention sweep. Nil unless -retention was passed.
	pruneSched *pruneScheduler
	// historyWire is built early, because the always-on pool must be given it
	// BEFORE its first Sync — see New.
	historyWire *historywire.Wire
	// connTrack is the fleet's connectivity debounce: who is OFFLINE, as
	// opposed to whose socket is shut this instant. See internal/connstate.
	connTrack *connstate.Tracker
	// connTick drives that debounce. See connstate.Tracker.TickAll.
	connTick *time.Ticker
	connStop chan struct{}
	// startedAt is when this process began serving, for /healthz's uptime and
	// its starting-vs-failing distinction.
	startedAt time.Time
	// holdFleet is whether a session is held for every router nobody is
	// watching, so their status is known and their alerts are evaluated. False
	// under `-no-pool`. See fleet_holds.go.
	holdFleet bool

	// conns is every live WebSocket connection, so a payload that must be built
	// PER PRINCIPAL can find the sessions to build it for.
	//
	// THE HUB CANNOT ANSWER THIS. It tracks `*hub.Client` — the write side of a
	// socket — and knows nothing about who is on the other end. `routers:update`
	// is filtered by what each viewer may read, so sending it needs the session,
	// and `BroadcastAll` would hand every viewer the same list.
	connsMu sync.Mutex
	conns   map[*hub.Client]*conn

	// devicesWatchers is who currently has the Devices page open. The pool
	// resumes on the FIRST and suspends on the LAST, so this is a count that
	// happens to name its members rather than a registry.
	devicesMu       sync.Mutex
	devicesWatchers map[*hub.Client]bool

	// scans holds every frequency scan running across the FLEET, not per router
	// and not per connection: the cap of three is fleet-wide, and the cooldown is
	// per operator. One registry for the process is what makes both mean anything.
	scans *wifiscan.Registry

	// auditDB may be nil: the app must still serve when the trail cannot be
	// opened. Every write is then unrecorded, which is reported once at startup
	// by whoever constructs the Server rather than on every event.
	auditDB *db.DB

	// store is kept to resolve a username to a user id: /api/auth/status
	// deliberately does not send one — "never the grant graph, which would
	// disclose every other principal's access to anyone who opened devtools" —
	// and users.json, which this process already reads, carries the mapping.
	store *store.Store
	// rbac answers the per-router question. Nil when the database could not be
	// opened, in which case the coarse gate stands alone; see (*conn).canPage.
	rbac *rbac.Resolver

	// ── AUTHENTICATION AFTER CUTOVER ────────────────────────────────────────
	//
	sessions4Web *websession.Store
	forceHTTPS   bool
	// staticDir is the shared asset tree; see Options.StaticDir.
	staticDir string
	// cities is the location picker's gazetteer: built on first search and
	// dropped after ten idle minutes. See internal/geo/cityholder.go.
	cities *geo.CityHolder

	// restoreTokens is the capability set for `/api/backups/:id/raw`, the one
	// route with no session behind it. On the SERVER because a token minted by
	// one connection is redeemed by a router on another — see
	// `internal/backups/restoretoken.go` for why the token is the entire gate.
	restoreTokens *backups.RestoreTokens

	// listenAddr is this process's own listen address, used to build the URL a
	// router fetches a backup from.
	listenAddr string

	// bkRunning is the routers being backed up right now, keyed by id.
	//
	// ON THE SERVER RATHER THAN THE CONNECTION, because the question the page
	// asks is "is this ROUTER busy", not "am I the one who started it". A second
	// operator opening the page mid-run has to see it too, or their Back Up Now
	// is enabled for work already in flight. The live app keeps the same set for
	// the same reason (`Backups._running` in src/backups/index.js).
	bkRunning sync.Map
}

// bkClaim marks a router as being backed up and reports whether it was free.
// The write queue already serialises the work; this makes it VISIBLE, and makes
// the second click a refusal the operator can see rather than a silent wait.
func (s *Server) bkClaim(routerID string) bool {
	_, loaded := s.bkRunning.LoadOrStore(routerID, struct{}{})
	return !loaded
}

func (s *Server) bkRelease(routerID string) { s.bkRunning.Delete(routerID) }

// bkIsRunning answers the page payload's `running` flag.
func (s *Server) bkIsRunning(routerID string) bool {
	_, ok := s.bkRunning.Load(routerID)
	return ok
}

func New(st *store.Store, opts Options) (*Server, error) {
	h := hub.New()

	srv := &Server{
		hub: h,
		// One per server, so the immutable-changelog cache is shared rather than
		// refetched once per viewer.
		changelog: changelog.New(),
		// Present from construction so `devicesFocus` never has to check. A nil
		// map here would panic on the first browser to open the page, which is
		// the one path guaranteed to be exercised.
		devicesWatchers: map[*hub.Client]bool{},
		conns:           map[*hub.Client]*conn{},
		// One registry for the process. See the field's comment: the fleet cap and
		// the per-operator cooldown are both meaningless if each connection keeps
		// its own.
		scans:        wifiscan.NewRegistry(func() int64 { return time.Now().UnixMilli() }),
		auth:         NewAuth(),
		staticDir:    strings.TrimSpace(opts.StaticDir),
		cities:       geo.NewCityHolder(opts.GeoDir),
		sessions4Web: websession.New(),
		forceHTTPS:   os.Getenv("FORCE_HTTPS") == "true",
		auditDB:      opts.AuditDB,
		store:        st,
		// Real time: the TTL is a security property, so it is not something a
		// caller may make generous.
		restoreTokens: backups.NewRestoreTokens(time.Now),
		listenAddr:    opts.ListenAddr,
		rbac: rbac.New(opts.AuditDB, func() []rbac.Router {
			// Read per query rather than captured, so a router added or moved
			// between sites while the process runs is seen on the next
			// question instead of the next restart.
			list, _ := st.Routers()
			out := make([]rbac.Router, 0, len(list))
			for _, r := range list {
				out = append(out, rbac.Router{ID: r.ID, SiteIDs: store.RouterSiteIDs(r),
					Label: r.Label, Host: r.Host})
			}
			return out
		}),
		sessions:       session.NewManager(st, h),
		web:            http.FileServer(http.Dir(opts.WebDir)),
		originPatterns: opts.OriginPatterns,
		writeLimit:     newWriteLimiter(),
		aiLimit:        newRateLimiter(20, time.Minute),
	}
	// Installed AFTER construction because it closes over the server. It is the
	// whole of authentication.
	srv.auth.SetLocal(srv.localSession)
	// The background pool. AFTER construction too — its identity hook closes
	// over the server, to write the record, record the audit event and broadcast
	// the new router list. See pool_wire.go for what gates it.
	// ── THE #105 ONE-SHOT, AT STARTUP AND BEFORE ANY POOL ─────────────────
	//
	// Live's `_migrateCollectionMode` is an IIFE that runs while index.js loads,
	// before any session is built. The order matters here for the same reason:
	// both pools resolve each router's collection config when they build their
	// sessions, so a migration running after them would leave the whole first
	// run on the pre-migration answer — the operator's Poll silently served as
	// Stream until the next restart.
	if srv.store != nil {
		if err := srv.store.MigrateCollectionMode(); err != nil {
			log.Printf("[store] collection migration: %v", err)
		}
	}
	srv.startedAt = time.Now()
	srv.pool = srv.buildPool(!opts.NoPool)
	// THE ALWAYS-ON HOLDS, sharing the same switch — see fleet_holds.go for why.
	// Unlike the overview pool these connect as soon as they are synced, so the
	// sync happens once here rather than waiting for a page.
	srv.holdFleet = !opts.NoPool
	if !srv.holdFleet {
		log.Printf("[holds] off; routers nobody is watching are neither connected " +
			"to nor alerted on (pass -no-pool to keep it that way)")
	}
	// ── THE HISTORY RECORDER GOES ON BEFORE THE FIRST SYNC ────────────────
	//
	// `Sync` is what BUILDS the sessions, and `buildCollectors` decides there
	// and then whether this session records — a pool synced before the recorder
	// was installed builds every session history-off and records nothing until
	// something forces a rebuild. Wiring it two hundred lines further down, next
	// to the session manager's copy, is exactly that bug.
	srv.historyWire = srv.buildHistoryWire(opts.History)
	// ── AND THE DEBOUNCE, HERE RATHER THAN WITH THE RECORDER ──────────────
	//
	// A session takes the tracker when it is BUILT, and `syncFleetHolds` below
	// builds one for every enabled router. `SetHistoryWire` — which this
	// replaces — was called a hundred lines further down, AFTER that sync, so
	// every held session took nil and the connect and drop of every router
	// nobody was watching reached no state machine at all. The same ordering
	// defect as the identity writer immediately below, which has its own note
	// and its own test for exactly this reason.
	//
	// ROWS AND VERDICTS GO TO DIFFERENT PLACES, which is the whole point of the
	// split: rows to the history wire, where reporting and `-history` decide
	// whether they are written, and the verdict to the badge and the alert,
	// which are true whether or not anything is being recorded.
	srv.connTrack = connstate.New(srv.historyWire.RecordConn, srv.connVerdict)
	srv.sessions.SetConnTracker(srv.connTrack)
	// ── AND THE IDENTITY WRITER, FOR THE SAME REASON ──────────────────────
	//
	// What each router says it is — the model, serial and version Settings →
	// Devices shows — is written back through this. A session takes it when it
	// is BUILT, so it must be attached before the sync below builds the held
	// ones: attached forty lines later, beside the alert sink, every held router
	// took nil and no version was ever written. See session.Manager.SetOnIdentity;
	// TestTheSessionManagersIdentityWriterIsAttached holds the ordering.
	srv.sessions.SetOnIdentity(srv.persistRouterIdentity)
	// `router:status` beyond a router's own room goes only to the browsers that
	// may read that router. See Server.sendFleetStatus.
	srv.sessions.SetFleetStatus(srv.sendFleetStatus)
	// ── AND THE OPERATOR'S OWN DOCUMENTS, FOR THE SAME REASON ─────────────
	//
	// The declared uplink list reaches the WAN collector through this. Attached
	// HERE rather than lower down because a session captures it at build time
	// and `syncFleetHolds` below builds every held one — see
	// session.Manager.SetDocSource.
	//
	// A READ FAILURE IS NIL, which the collector reads as "nothing declared" and
	// falls back to the router's own detection. That is the right way round: a
	// database blip must not empty the page.
	srv.sessions.SetDocSource(func(routerID, kind string) []byte {
		blob, err := srv.auditDB.Doc(routerID, kind)
		if err != nil {
			return nil
		}
		return blob
	})
	// SYNCED AT STARTUP. `New` connects to nothing; `Sync` does. The overview
	// pool can wait for `devicesFocus` because its rows are only wanted while
	// that page is open — this one exists so a router nobody is watching is
	// still known to be up and still has its alerts evaluated, which is a claim
	// about the whole uptime of the process.
	srv.syncFleetHolds()
	// ── AND THE CLOCK THE DEBOUNCE NEEDS ──────────────────────────────────
	//
	// `history.Connectivity` holds no timer: the caller supplies the passage of
	// time, which is what makes its rules testable without one. Nothing supplied
	// it, so a non-zero `connDownThresholdSec` could never fire and the only
	// workable threshold was zero — record every close, at once. A routine
	// six-second reconnect then appeared in the Reports page as an outage.
	//
	// The cadence and why it is no longer gated on `-history` are on
	// startConnTicker itself.
	srv.startConnTicker()
	// ── AND AGAIN WHEN A SESSION FINALLY GOES ─────────────────────────────
	//
	// A session now outlives its last viewer by `session.DefaultIdleGrace`, so
	// "the browser closed" and "this router is uncovered" are two moments up to
	// two minutes apart. The pool must reclaim the router at the SECOND one:
	// `syncFleetHolds` excludes anything in `sessions.Live()`, so calling it at
	// Release time skips the very router that is about to need covering, and
	// nothing would call it again.
	srv.sessions.SetOnIdle(func(string) { srv.syncFleetHolds() })
	// The alert evaluator, for the same reason and in the same place: it needs
	// the settings and the history database, both of which exist only now.
	//
	// IT WRITES ROWS AND DISPATCHES NOTHING. See alert_wire.go.
	srv.alerts = srv.buildAlertWire()
	srv.sessions.SetAlertWire(srv.alerts)
	srv.dispatch = srv.buildAlertDispatch(opts.AlertDispatch)
	// The trusted proxy list, before anything serves a request: the rate limiters
	// and the audit trail resolve clients through clientIPOf. A copy, so nothing
	// the caller does to its slice afterwards changes who is trusted.
	trusted := append([]netip.Prefix(nil), opts.TrustedProxies...)
	trustedProxies.Store(&trusted)
	// AND THE SINK THAT ACTUALLY USES IT. Attached unconditionally: the
	// dispatcher itself is the switch — `Deliver` returns false when disabled and
	// leaves no cooldown trace — so a build with the flag off wires an inert
	// path rather than a missing one.
	srv.sessions.SetAlertSink(srv.dispatchFired)
	// The backup scheduler, off unless asked for. Two schedulers against one
	// fleet take two backups of every router on the same timetable.
	srv.backupSched = srv.buildBackupScheduler(opts.BackupScheduler)
	// ── AND STARTED, which until 2026-08-29 nothing did ───────────────────
	//
	// `buildBackupScheduler` returned a scheduler that never ticked, so
	// `-backup-scheduler` switched on a component that could not act. That was
	// correct while the flag was a placeholder for a cutover step; it stops being
	// correct the moment an operator passes the flag and reasonably expects
	// backups.
	//
	// The interval is the scheduler's own default, which is the live `TICK_MS`
	// of five minutes (`src/backups/index.js:36`). The tick only ASKS whether a
	// router is due; `IsDue` is what decides, and it is pinned against the live
	// implementation by the backup-due corpus.
	//
	// Nil when the flag is off, and `Start` on a nil scheduler would panic — so
	// the guard is not decoration.
	if srv.backupSched != nil {
		srv.backupSched.Start(0)
	}
	// The report scheduler, behind the switch for sending messages. See
	// report_scheduler.go.
	srv.reportSched = srv.buildReportScheduler(opts.AlertDispatch)
	if srv.reportSched != nil {
		srv.reportSched.Start()
	}
	// ── THE DAILY RETENTION SWEEP ─────────────────────────────────────────
	//
	// STANDALONE, with no flag, and deliberately unlike the three switches above
	// it. Those are off by default because they ACT ON THE FLEET — a second
	// process taking backups, sending notifications or bucketing history does
	// visible damage alongside Node. This one deletes rows this process's own
	// database no longer needs, and Node runs the identical sweep when it is
	// there, so the only question is which process owns an install-wide policy.
	//
	// Until 2026-08-29 nothing pruned at all: the Settings page rendered
	// `dbRetentionDays`, `dbAlertRetentionDays` and `dbAuditRetentionDays`, the
	// write route validated and persisted them, and no code read one. The
	// database grew without bound while the UI implied a policy.
	// BEHIND ITS FLAG, off by default: the sweep DELETES, so it is the one switch
	// where a default-on mistake cannot be undone.
	srv.pruneSched = srv.buildPruneScheduler(opts.Retention)
	// The traffic and ping recorder, off the session's emit seam. CONNECTIVITY
	// is no longer part of it — that is the tracker's, attached at the top of
	// New, where the ordering is correct.
	hw := srv.historyWire
	srv.sessions.SetHistoryWire(hw)
	// ── AND THE POOL RECORDS TOO, WHEN -history IS ON ─────────────────────
	//
	// Under `-history` the port used to record ONLY while a browser had the
	// router selected, because `historywire` is fed from the session's emit
	// closure and a `Session` exists only while a socket wants one. MEASURED
	// 2026-08-29: live wrote a steady 60 traffic rows an hour with nobody
	// logged in and this port wrote between 5 and 44, tracking browser activity.
	//
	// NO NEW FLAG, on the operator's decision of 2026-08-30: `-history` meaning
	// "incomplete history" was the real defect, so completeness comes with the
	// flag that is already there.
	//
	// It costs no new connection. `syncPool` excludes exactly the routers that
	// have a live `Session`, so the ACTIVE router is pooled precisely when no
	// browser is watching it — the window where history was missing — and the
	// socket is already established. Two command channels, one router, and only
	// while nobody is looking; the moment a browser attaches, the session takes
	// the router out of the pool and records it itself.
	// ── CONTINUOUS HISTORY GOES ON THE ALWAYS-ON HOLD ─────────────────────
	//
	// `syncFleetHolds` runs at startup and holds a session for every enabled
	// router whether or not anyone is looking. `internal/routers.Pool` is synced
	// from the Devices page and the routers API only, so it idles until somebody
	// looks at something — wiring history there recorded nothing after a restart
	// with no browser, measured 2026-08-30.
	//
	// Both are wired: the `history` hold is what makes history CONTINUOUS, and
	// the routers pool covers the window where the Devices page is open. The
	// hold used to be a session in `internal/alertpool`; a session that records
	// its own history is the same seam with one implementation instead of two.
	if hw.Enabled() {
		if srv.pool != nil {
			srv.pool.WithHistory(hw.Record)
			// `syncHistoryRouter` was here. Recording is each router's own
			// setting now, applied by `Pool.Sync` itself, so there is nothing
			// to push separately at startup.
		}
	}
	return srv, nil
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", s.handleWS)
	// The report endpoints. Registered BEFORE the catch-all — ServeMux prefers
	// the longer pattern regardless of order, but relying on that is how a route
	// quietly becomes a 404 page.
	s.registerReports(mux)
	s.registerAudit(mux)
	s.registerBackupRaw(mux)
	s.registerBackupDownloads(mux)
	s.registerAuthLogin(mux)
	// The first-run wizard. See setup_api.go.
	s.registerSetup(mux)
	// The account modal: sessions, access, permissions, password. See
	// account_api.go.
	s.registerAccount(mux)
	// `/api/auth/status`: the login page asks it before showing the form, and
	// the SPA asks it for its first paint.
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	s.registerSettings(mux)
	// The install's name and icon (issue #131). Its reads are public: the login
	// page shows them before anybody signs in. See branding_api.go.
	s.registerBranding(mux)
	s.registerUserNotify(mux)
	s.registerPrincipals(mux)
	// The principal writes.
	s.registerUsersWrite(mux)
	s.registerGroupsWrite(mux)
	s.registerRolesWrite(mux)
	s.registerGrantsWrite(mux)
	// The database cleanup card.
	s.registerDBAdmin(mux)
	// The four notification Test buttons.
	s.registerTestNotification(mux)
	// The AI Agent tab's Test button.
	s.registerAITest(mux)
	s.registerHealth(mux)
	s.registerNavPrefs(mux)
	s.registerLocalCC(mux)
	s.registerCities(mux)
	s.registerLayouts(mux)
	s.registerRouterDocs(mux)
	s.registerDNSFleet(mux)
	s.registerTopologyFleet(mux)
	s.registerAlerts(mux)
	s.registerRouters(mux)
	s.registerRouterTest(mux)
	s.registerSites(mux)

	// ── THE SHARED ASSETS, AND AN HONEST 404 FOR EVERYTHING ELSE ─────────
	//
	// The catch-all: a file from the static tree when it holds one, 404 when it
	// does not (or when no tree is configured).
	mux.Handle("/", s.staticOrNotFound())
	// ── THE APP LIVES AT THE ROOT. THERE IS NO PREFIX ──────────────────────
	//
	// It used to live under `/next`, which was COEXISTENCE SCAFFOLDING: a second
	// mount point, so Node could keep `/` while this port took one page at a
	// time. The operator asked why the URL carried it when the live app's does
	// not, and the honest answer was "it should not" — so on 2026-08-28 the
	// prefix was removed outright rather than kept as an alias. "We won't use
	// it": an alias nobody uses is a second code path nobody tests, and this
	// project has already been bitten by exactly that (see `Prefix` in the
	// history — every server-side check asked for `/next/` and the root answered
	// 502 for an unknown length of time).
	//
	// WHAT MADE IT POSSIBLE was making the asset references ABSOLUTE in
	// `build.mjs`. They were `./app.js`, relative to the mount point, which is
	// why the first attempt at this was a redirect: served at the root, a
	// relative reference resolves to `/app.js` and nothing served it. The
	// document now names `/app.js` and `/app.css` outright.
	mux.Handle("/{$}", s.requireSession(s.spa()))
	mux.Handle("/app.js", s.requireSession(s.spa()))
	mux.Handle("/app.css", s.requireSession(s.spa()))

	// ── A URL PER PAGE ─────────────────────────────────────────────────
	//
	// `spa()` already rewrites an extensionless path to `/` and serves the
	// built document, so every page gets the same shell and the frontend
	// router decides what to show from the path.
	//
	// REGISTERED ONE BY ONE, not as a catch-all, and that is the whole
	// point: an unknown path still reaches `/` and answers an honest 404
	// instead of the shell. A catch-all would also have to re-derive the
	// reserved list -- /api, /ws, /healthz, /login, /preflight.js, /vendor,
	// /css, /fonts -- that this gets for free from the mux preferring the
	// more specific pattern.
	for _, p := range pages.All {
		mux.Handle("/"+p.URL(), s.requireSession(s.spa()))
	}

	// ── THE LOGIN DOCUMENT AND THE TWO CLASSIC SCRIPTS ─────────────────
	//
	// NOT session-gated, and that is the point: `/login` is where an
	// unauthenticated browser is SENT, so gating it would be a redirect
	// loop. `preflight.js` is in the <head> of the app shell and runs before
	// anything has been validated.
	//
	// Served from `dist` rather than from the static tree because they are
	// now BUILT — `web/src/entry/login.ts` and `web/src/entry/preflight.ts`. They were
	// byte-for-byte copies of the live repo's files under `web/public`
	// until 2026-08-28, when the operator asked that the port "stand on its
	// own without any lingering JS from the live repo". Registering them
	// here is what makes the copies unreachable, so deleting them cannot
	// silently leave the old ones being served.
	mux.Handle("/login", s.distFile("/login.html"))
	mux.Handle("/login.js", s.distFile("/login.js"))
	mux.Handle("/preflight.js", s.distFile("/preflight.js"))
	return logRequests(mux)
}

// spa serves the built frontend, falling back to index.html so an extensionless
// path is routed by the client rather than 404ing.
func (s *Server) spa() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// REVALIDATED ON EVERY LOAD. The shell, `app.js` and `app.css` keep their
		// names across builds and carry only Last-Modified, which lets a browser
		// keep running the copy it already has after a rebuild. `no-cache` still
		// uses that copy once the server confirms it is current (a 304), so a
		// reload costs a round trip rather than a download.
		w.Header().Set("Cache-Control", "no-cache")
		if ext := path.Ext(r.URL.Path); ext == "" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		s.web.ServeHTTP(w, r)
	})
}

// distFile serves one named file out of the built directory.
//
// NOT `spa()`, and the difference is a trap: `spa` rewrites any EXTENSIONLESS
// path to "/" so a client-routed deep link reaches the app shell. `/login` is
// extensionless, so routing it through `spa` would serve `index.html` — the
// dashboard — to somebody with no session, which is both the wrong page and a
// disclosure.
func (s *Server) distFile(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.URL.Path = name
		s.web.ServeHTTP(w, r)
	})
}

// requireSession sends an unauthenticated browser to the login page rather than
// to a shell that would immediately fail to open a socket.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.auth.Validate(r.Header.Get("Cookie")); err != nil {
			http.Redirect(w, r, loginFor(r), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginFor is where an unauthenticated request is sent, carrying where it was
// going.
//
// Deep links only became possible on 2026-09-01, and without this every one of
// them landed the operator on the dashboard after signing in -- having asked for
// `/logs`. The login page already reads `?next=`, validates it is same-origin and
// falls back to `/`, so nothing new is needed on the other side.
//
// THE ROOT IS EXEMPT, and not incidentally: `/` is where an unauthenticated
// visitor arrives anyway, so `?next=/` would be noise on the commonest redirect
// in the app -- and `TestStandaloneServesTheAppFromTheRoot` pins the bare
// `/login` that visitor gets.
func loginFor(r *http.Request) string {
	if r.URL.Path == "/" {
		return "/login"
	}
	dest := r.URL.Path
	if r.URL.RawQuery != "" {
		dest += "?" + r.URL.RawQuery
	}
	return "/login?next=" + url.QueryEscape(dest)
}

// Shutdown releases everything this process holds, in the order that loses the
// least.
//
// ── IT USED TO BE ONE LINE, AND THREE THINGS OUTLIVED IT ──────────────────
//
// `s.sessions.Shutdown()` alone left the two background pools holding open
// sockets, the backup scheduler ticking, and the database unclosed. The process
// exits immediately afterwards so nothing ran for long — but a SQLite handle
// closed by process death has not checkpointed its WAL, and a router sees a
// dropped TCP connection rather than a close.
//
// Live does the equivalent in `shutdown()`: stops the collectors, flushes,
// `db.close()`, then closes the server.
//
// ORDER MATTERS AND IS NOT ALPHABETICAL:
//
//	scheduler first  so a tick cannot start a backup into a closing database.
//	sessions next    they FLUSH the open history minute, which needs the db.
//	pools after      nothing else depends on them; they only hold sockets.
//	database last    everything above may still write.
//
// startConnTicker drives the connectivity debounce.
//
// ── IT IS NO LONGER GATED ON `-history`, AND THAT WAS THE WHOLE DEFECT ─────
//
// The ticker used to start only when recording was on, because recording was
// the only consumer. The debounce decides the fleet's Online/Offline badge and
// the Router Offline / Online alert now, so on a default install — `-history`
// is off by default — a gated ticker would mean no outage is ever DECLARED:
// every drop would sit as a pending timer nothing advances, and the threshold
// would silently mean "never".
//
// ONE SECOND, for the whole fleet. The threshold is measured in seconds and the
// sweep is a map walk plus a comparison per router.
func (s *Server) startConnTicker() {
	s.connTick = time.NewTicker(time.Second)
	s.connStop = make(chan struct{})
	go func() {
		for {
			select {
			case <-s.connStop:
				return
			case t := <-s.connTick.C:
				s.connTrack.TickAll(t.UnixMilli())
			}
		}
	}()
}

func (s *Server) Shutdown() {
	// STOPPED FIRST, and this is the sibling of the retention sweep below: a
	// ticker assigned and never stopped outlives the server, which is harmless
	// at process exit and a goroutine leak in every test that builds one.
	if s.connTick != nil {
		s.connTick.Stop()
		close(s.connStop)
		s.connTick = nil
	}
	if s.backupSched != nil {
		s.backupSched.Stop()
	}
	if s.reportSched != nil {
		s.reportSched.Stop()
	}
	// ── AND THE RETENTION SWEEP, WHICH NOTHING STOPPED ────────────────────
	//
	// `pruneSched` was assigned in `New` and never read anywhere, so its `Stop`
	// existed and was unreachable and its daily ticker goroutine outlived the
	// server. Found by counting Server's fields for ones that are ASSIGNED and
	// NEVER READ — Go does not flag that for a struct field, and the sibling two
	// lines above was stopped correctly the whole time.
	//
	// Harmless at process exit, which is when a real deployment shuts down. Not
	// harmless in tests, where every `Server` built leaked a goroutine holding a
	// 24-hour ticker.
	if s.pruneSched != nil {
		s.pruneSched.Stop()
	}
	s.sessions.Shutdown()
	// ── THE OPEN MINUTE, BEFORE THE CONNECTIONS GO ────────────────────
	//
	// A history bucket only rolls over when the NEXT minute's first sample
	// arrives, so a process that stops mid-minute leaves that minute unwritten.
	// `internal/session` has always flushed for exactly this reason — its own
	// header quotes live's "flush all open buckets — call on session teardown to
	// avoid data loss".
	//
	// The BACKGROUND path had no such call, and it is the PRIMARY recorder: it
	// is what records while nobody is watching, which is almost always. So every
	// restart silently lost the minute in progress. Added with the pool half of
	// LOOP.md 0i, and missed until the flush call sites were counted.
	//
	// BEFORE `pool.Close()`, so the overview pool's collectors are still there
	// to have produced what is being flushed. The background recorder is a HELD
	// SESSION now rather than `internal/alertpool`, and `sessions.Shutdown()`
	// flushes each one as it tears it down — so the ordering that matters for
	// that half is inside the manager, not here.
	if s.historyWire.Enabled() {
		// EVERY RECORDING ROUTER, not one. This was `Flush(HistoryRouter())`
		// back when a single router recorded; with several, naming one would
		// lose the open minute for all the others on every restart.
		s.historyWire.FlushAll()
	}
	if s.pool != nil {
		s.pool.Close()
	}
	if s.auditDB != nil {
		if err := s.auditDB.Close(); err != nil {
			log.Printf("[shutdown] closing the database: %v", err)
		}
	}
}

// logRequests logs what this process actually answers.
//
// It used to filter on the `/next` prefix, which was a neat proxy for "requests
// the port serves" while everything else was Node's. With the prefix gone that
// filter matched nothing, so the choice had to be made explicitly rather than
// left to decay into a log that never prints.
//
// EXTENSIONLESS AND `/api` ONLY: page loads and API calls, which is the traffic
// worth reading. The asset tree — the stylesheet, the vendor bundles, the logo —
// is dozens of lines per page load and would bury it.
//
// AND NOT `/healthz`. The container healthcheck probes it every 30 seconds, which
// is 2,880 identical lines a day — the same burying, by a caller that is not a
// user. A probe answering is not news; a probe FAILING shows up as the container
// going unhealthy, which is where an operator would look for it anyway.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if path.Ext(r.URL.Path) == "" || strings.HasPrefix(r.URL.Path, "/api") {
			log.Printf("[http] %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
