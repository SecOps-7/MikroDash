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
	"mikrodash/internal/alertpool"
	"mikrodash/internal/alertwire"
	"mikrodash/internal/geo"
	"mikrodash/internal/websession"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/changelog"
	"mikrodash/internal/db"
	"mikrodash/internal/historywire"
	"mikrodash/internal/hub"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routers"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
	"mikrodash/internal/wifiscan"
)

// Options configures the server.
type Options struct {
	// NodeURL is the Node app this proxies to and delegates sessions to.
	// EMPTY means standalone: nothing to proxy to, and Go owns authentication.
	NodeURL string
	// GeoDir is where geoip-lite keeps its data, for the location picker's
	// gazetteer. Empty means the picker reports itself unavailable, which is a
	// supported state rather than an error.
	GeoDir string
	// NoPool turns the background pool OFF in a standalone process that would
	// otherwise run one.
	//
	// The pool holds a connection to every router NOBODY has open, and the
	// documented bottleneck on a MikroTik is concurrent API channels. Standalone
	// normally means "there is no other pool", which is why the pool is bound to
	// it — but running this process beside the live app to compare them breaks
	// that implication, and then both pools are live against the same hardware.
	// This is the way out, and it costs the Devices page its rows for unwatched
	// routers rather than breaking anything.
	NoPool bool
	// AlertDispatch turns alert NOTIFICATIONS on. It defaults OFF, and the
	// default is the decision — not an oversight to be tidied up later.
	//
	// The evaluator and the database writes run regardless: they are idempotent
	// within this install's own history, and they are what make the Alerts page
	// and the Devices alert counts real. Sending is different. `PORT-QUEUE.md`
	// blocker 5:
	//
	//	Both engines evaluate the same conditions against the same physical
	//	routers, and the cooldown is an in-memory map rather than a shared row,
	//	so neither sees the other's sends. A duplicated Telegram message or
	//	email cannot be un-received.
	//
	// A row filed twice is a duplicate an operator deletes. A message sent twice
	// is already in their pocket. So this stays off until the operator says
	// otherwise, and after cutover it is simply on.
	AlertDispatch bool
	// BackupScheduler turns SCHEDULED backups on. Off by default, for the same
	// reason as the dispatch: during coexistence Node is already taking them,
	// and two schedulers means two backups per router per schedule, each holding
	// a router channel while it runs.
	BackupScheduler bool

	// Retention turns the daily database sweep on. Off by default: it DELETES,
	// and `standalone` is not by itself evidence that this process owns the
	// database it is pointed at.
	Retention bool
	// History turns the traffic/ping/connectivity RECORDING on. Off by default:
	// two processes bucketing the same samples double every minute row, and
	// Reports averages by minute, so the damage is a plausible wrong chart.
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
	// OriginPatterns are accepted Origin headers for the WebSocket handshake.
	// Empty means same-origin only, which is what a reverse-proxied deployment
	// wants.
	OriginPatterns []string
	// AuthTTL bounds how long a validated session is cached.
	AuthTTL time.Duration
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
	hub            *hub.Hub
	auth           *Auth
	sessions       *session.Manager
	proxy          *httputil.ReverseProxy
	web            http.Handler
	originPatterns []string

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
	// The daily retention sweep. Nil unless this process is standalone.
	pruneSched *pruneScheduler
	// historyWire is built early, because the always-on pool must be given it
	// BEFORE its first Sync — see New.
	historyWire *historywire.Wire
	// startedAt is when this process began serving, for /healthz's uptime and
	// its starting-vs-failing distinction.
	startedAt time.Time
	// alertPool holds a connection to every router nobody is watching, so their
	// status is known and their alerts are evaluated. Nil when `-no-pool`.
	alertPool *alertpool.Pool

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
	// standalone is "there is no Node to delegate to" — see auth_login.go for
	// why these three exist and why they are conditional.
	standalone   bool
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
	nodeURL, err := url.Parse(opts.NodeURL)
	if err != nil {
		return nil, err
	}
	ttl := opts.AuthTTL
	if ttl == 0 {
		ttl = 15 * time.Second
	}
	h := hub.New()

	proxy := httputil.NewSingleHostReverseProxy(nodeURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// The Node app being down must read as "the half that has not been
		// ported is unavailable", not as a blank page with no explanation.
		log.Printf("[proxy] %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "the MikroDash Node app is not reachable", http.StatusBadGateway)
	}

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
		scans: wifiscan.NewRegistry(func() int64 { return time.Now().UnixMilli() }),
		auth:  NewAuth(opts.NodeURL, ttl),
		// STANDALONE IS "no Node to delegate to", which is what cutover means.
		// Derived rather than configured: a separate flag could disagree with
		// the proxy target, and then auth and routing would have different
		// ideas about whether Node exists.
		standalone:   strings.TrimSpace(opts.NodeURL) == "",
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
		proxy:          proxy,
		web:            http.FileServer(http.Dir(opts.WebDir)),
		originPatterns: opts.OriginPatterns,
	}
	// Installed AFTER construction because it closes over the server. In
	// standalone mode this is the whole of authentication; while Node runs it
	// stays nil and `Auth.Validate` asks Node exactly as before.
	if srv.standalone {
		srv.auth.SetLocal(srv.localSession)
	}
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
	// ── STANDALONE ONLY, AND THIS IS NOT DEFENSIVENESS ────────────────────
	//
	// The migration WRITES: router records, then settings.json. `tools/live-diff.sh`
	// stands a Go server up against the LIVE /data to diff payloads, and it
	// passes `-node`, so this process is a proxy rather than the app. A proxy is
	// not the owner of that directory and must not migrate it.
	//
	// Today the live install is already migrated, so the flag check returned
	// early and the diff run wrote nothing — VERIFIED by mtime, both files
	// untouched. That is luck rather than design, and it is the same shape as
	// the `-retention` gate: a verification run must not be able to act.
	//
	// It also matches where live puts it. `_migrateCollectionMode` is an IIFE in
	// `index.js` — the APP — while the router seed lives in `routers.js` data
	// access and therefore still runs for any reader, exactly as live's does.
	if srv.standalone && srv.store != nil {
		if err := srv.store.MigrateCollectionMode(); err != nil {
			log.Printf("[store] collection migration: %v", err)
		}
	}
	srv.startedAt = time.Now()
	srv.pool = srv.buildPool(srv.standalone && !opts.NoPool)
	// The ALWAYS-ON pool, sharing the same switch — see buildAlertPool for why.
	// Unlike the overview pool this one connects as soon as it is synced, so it
	// is synced once here rather than waiting for a page.
	srv.alertPool = srv.buildAlertPool(srv.standalone && !opts.NoPool)
	// ── THE HISTORY RECORDER GOES ON BEFORE THE FIRST SYNC ────────────────
	//
	// `Sync` is what BUILDS the sessions, and `buildCollectors` decides there
	// and then whether this session records — a pool synced before the recorder
	// was installed builds every session history-off and records nothing until
	// something forces a rebuild. Wiring it two hundred lines further down, next
	// to the session manager's copy, is exactly that bug.
	srv.historyWire = srv.buildHistoryWire(opts.History)
	if srv.historyWire.Enabled() && srv.alertPool != nil {
		srv.alertPool.WithHistory(srv.historyWire.Record)
	}
	// SYNCED AT STARTUP. `New` connects to nothing; `Sync` does. The overview
	// pool can wait for `devicesFocus` because its rows are only wanted while
	// that page is open — this one exists so a router nobody is watching is
	// still known to be up and still has its alerts evaluated, which is a claim
	// about the whole uptime of the process.
	srv.syncAlertPool()
	// The alert evaluator, for the same reason and in the same place: it needs
	// the settings and the history database, both of which exist only now.
	//
	// IT WRITES ROWS AND DISPATCHES NOTHING. See alert_wire.go.
	srv.alerts = srv.buildAlertWire()
	srv.sessions.SetAlertWire(srv.alerts)
	srv.dispatch = srv.buildAlertDispatch(opts.AlertDispatch)
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
	// implementation by `tools/backup-due-cases.js`.
	//
	// Nil when the flag is off, and `Start` on a nil scheduler would panic — so
	// the guard is not decoration.
	if srv.backupSched != nil {
		srv.backupSched.Start(0)
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
	// STANDALONE **AND** THE FLAG. `standalone` alone was not enough: it means
	// only "no -node was passed", and `tools/live-diff.sh` stands a server up
	// against the LIVE /data. The sweep DELETES, so it is the one switch of the
	// four where a default-on mistake cannot be undone.
	srv.pruneSched = srv.buildPruneScheduler(srv.standalone && opts.Retention)
	// The history recorder. Installed on the session manager even when disabled,
	// so the call sites in session.go run on every tick rather than for the first
	// time inside the cutover window.
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
	// ── CONTINUOUS HISTORY GOES ON THE ALWAYS-ON POOL ─────────────────────
	//
	// `internal/alertpool` is synced at startup and holds a connection to every
	// enabled router whether or not anyone is looking. `internal/routers.Pool`
	// is synced from the Devices page and the routers API only, so it idles
	// until somebody looks at something — wiring history there recorded nothing
	// after a restart with no browser, measured 2026-08-30.
	//
	// Both are wired: the alertpool is what makes history CONTINUOUS, and the
	// routers pool covers the window where an interactive session has taken the
	// router away from the alertpool but the Devices page is what is open.
	if hw.Enabled() {
		if srv.pool != nil {
			srv.pool.WithHistory(hw.Record)
			srv.syncHistoryRouter()
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
	s.registerCollectors(mux)
	s.registerBackupRaw(mux)
	// ── ONLY WITHOUT A NODE TO PROXY TO ─────────────────────────────────
	//
	// See auth_login.go. Registering these while Node runs would stop
	// `/api/auth/login` reaching it, so the browser would hold a Go session
	// Node does not know and every unported page would answer 401 — a bug
	// that looks like "the login works but half the app logged me out".
	if s.standalone {
		s.registerAuthLogin(mux)
		// The first-run wizard WRITES users.json, which Node caches and never
		// re-reads — so with both processes up, both would see zero users and
		// both would mint a first administrator. See setup_api.go.
		s.registerSetup(mux)
		// The account modal's session list and its revoke button read the
		// session store this process owns, which is empty while Node is the
		// authority. See account_api.go.
		s.registerAccount(mux)
		// The password change WRITES users.json, which Node caches and would
		// revert — so it registers only where Node is not running.
		s.registerAccountPassword(mux)
	}
	// `/api/auth/status` too: the login page asks it before showing the form,
	// and the SPA asks it for its first paint. Proxied while Node runs.
	if s.standalone {
		mux.HandleFunc("GET /api/auth/status", s.authStatus)
	}
	s.registerSettings(mux)
	s.registerUserNotify(mux)
	s.registerPrincipals(mux)
	if s.standalone {
		// The principal WRITES. Standalone only, and the reason is the same one
		// that gates the login routes: Node caches its RBAC views on a generation
		// counter only its own bump() advances, so a Go write while it runs would
		// leave it honouring a revoked grant until it restarted.
		s.registerUsersWrite(mux)
		s.registerGroupsWrite(mux)
		s.registerRolesWrite(mux)
		s.registerGrantsWrite(mux)
		// The database cleanup card. Standalone for a DIFFERENT reason from the
		// principal writes above: nothing here is cached by Node, but a purge
		// deletes history the running Node app is still collecting into, and the
		// two would race on the same file.
		s.registerDBAdmin(mux)
		// The four Test buttons. Standalone-only for the same shape of reason,
		// though not the same reason: this one SENDS, and while both apps run
		// there are two Settings pages that could send. One message per press is
		// harmless — see the file header on why blocker 5 does not reach it — but
		// the button belongs to the app the operator is actually configuring.
		s.registerTestNotification(mux)
		s.registerHealth(mux)
	}
	s.registerNavPrefs(mux)
	s.registerLocalCC(mux)
	s.registerAccountAccess(mux)
	s.registerAuthPermissions(mux)
	s.registerCities(mux)
	s.registerLayouts(mux)
	s.registerAlerts(mux)
	s.registerRouters(mux)
	s.registerRouterTest(mux)
	s.registerSites(mux)

	// ── THE SHARED ASSETS, WHEN THIS PROCESS HAS TO SERVE THEM ──────────
	//
	// Registered BEFORE the catch-all and only when a directory is configured,
	// so an install that is still proxying behaves exactly as it did. The
	// handler falls through to the proxy for anything the directory does not
	// hold, which keeps a partial asset tree from turning into a wall of 404s
	// mid-migration.
	if s.staticDir != "" {
		mux.Handle("/", s.staticOrProxy())
	} else {
		// Everything else is still Node's.
		mux.Handle("/", s.proxy)
	}
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
	//
	// STILL GATED ON `standalone`, and that is not a leftover: `tools/live-diff.sh`
	// runs this binary ALONGSIDE the live app to compare their payloads endpoint
	// by endpoint, and it logs in through this process's proxy. Taking `/` away
	// from Node would break the one tool that measures the two against each
	// other. With a Node URL configured, this process serves APIs and proxies the
	// rest, exactly as before — it just no longer offers a frontend of its own.
	if s.standalone {
		mux.Handle("/{$}", s.requireSession(s.spa()))
		mux.Handle("/app.js", s.requireSession(s.spa()))
		mux.Handle("/app.css", s.requireSession(s.spa()))

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
	}
	return logRequests(mux)
}

// spa serves the built frontend, falling back to index.html so an extensionless
// path is routed by the client rather than 404ing.
func (s *Server) spa() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
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
func (s *Server) Shutdown() {
	if s.backupSched != nil {
		s.backupSched.Stop()
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
	// ── THE POOL'S OPEN MINUTE, BEFORE ITS CONNECTIONS GO ─────────────────
	//
	// A history bucket only rolls over when the NEXT minute's first sample
	// arrives, so a process that stops mid-minute leaves that minute unwritten.
	// `internal/session` has always flushed for exactly this reason — its own
	// header quotes live's "flush all open buckets — call on session teardown to
	// avoid data loss" — and `sessions.Shutdown()` above does it for the
	// browser-driven path.
	//
	// The POOL path had no such call, and since 2026-08-30 it is the PRIMARY
	// recorder: it is what records while nobody is watching, which is almost
	// always. So every restart silently lost the minute in progress. Added with
	// the pool half of LOOP.md 0i, and missed until the flush call sites were
	// counted.
	//
	// BEFORE `alertPool.Close()`, so the collectors are still there to have
	// produced what is being flushed.
	if s.historyWire.Enabled() && s.alertPool != nil {
		s.historyWire.Flush(s.alertPool.HistoryRouter())
	}
	if s.alertPool != nil {
		s.alertPool.Close()
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
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Ext(r.URL.Path) == "" || strings.HasPrefix(r.URL.Path, "/api") {
			log.Printf("[http] %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
