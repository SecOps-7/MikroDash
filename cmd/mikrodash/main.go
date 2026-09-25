// Command mikrodash is the MikroDash server: the router sessions and their
// collectors, the HTTP routes and the WebSocket, and the built frontend, in one
// process.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mikrodash/internal/asn"
	"mikrodash/internal/db"
	"mikrodash/internal/geo"
	"mikrodash/internal/roslimit"
	"mikrodash/internal/server"
	"mikrodash/internal/store"
	"mikrodash/internal/trustedproxy"
)

func main() {
	var (
		listen = flag.String("listen", ":3082", "address to serve on")
		// ── RETIRED: THE NODE COEXISTENCE MODE ─────────────────────────────
		//
		// `-node` named a Node MikroDash to proxy unported routes to and ask for
		// sessions, for the strangler-fig migration. The mode was removed on
		// 2026-09-19. The flag is still ACCEPTED because shipped compose files
		// and the old image CMD pass `-node=` (empty), and an unknown flag would
		// stop the container starting; a non-empty value is refused below.
		node      = flag.String("node", "", "retired: must be empty (the Node coexistence mode was removed)")
		staticDir = flag.String("static", "web/public",
			"the vendored asset tree (/vendor, /css, /fonts, /logo.png, the login page). "+
				"Empty serves none of them")
		data   = flag.String("data", "/data", "the MikroDash data directory")
		webDir = flag.String("web", "web/dist", "the built frontend")
		// The background pool runs by default: it holds the routers nobody has
		// open, for the Devices page and for alerting. A second process pointed
		// at a fleet another MikroDash already polls would double the channels
		// held on the same hardware, and API channels are the documented
		// bottleneck. This is the way out.
		noPool = flag.Bool("no-pool", false,
			"do not run the background router pool (use when another MikroDash is polling the same fleet)")
		// OFF UNLESS PASSED (the image passes it). The alert EVALUATOR always
		// runs and writes its rows; this switch controls only whether a
		// notification is SENT. Two processes watching the same routers would
		// each send, with cooldowns neither can see, and a message cannot be
		// unsent.
		alertDispatch = flag.Bool("alert-dispatch", false,
			"SEND alert notifications (off by default; the evaluator and its database "+
				"rows run either way). Do not enable while another MikroDash watches the "+
				"same routers: both would send.")
		// OFF UNLESS PASSED, like -alert-dispatch and for the same reason: two
		// schedulers mean two restore points per router per schedule, each
		// holding a router channel while it runs.
		backupSched = flag.Bool("backup-scheduler", false,
			"take SCHEDULED backups (off by default). Do not enable while another "+
				"MikroDash is backing up the same routers.")
		// OFF BY DEFAULT, like the two above. Two processes bucketing the same
		// per-second samples write two rows per minute per interface, and
		// Reports averages by minute — a plausible chart with wrong numbers.
		historyOn = flag.Bool("history", false,
			"record traffic, ping and connectivity history (off by default). Do not "+
				"enable while another MikroDash is recording the same routers.")
		// OFF UNLESS PASSED, like the three above, AND FOR A SHARPER REASON: it
		// is the only one of the four that DELETES, so it must be asked for.
		pruneOn = flag.Bool("retention", false,
			"age rows out of the database per the dbRetentionDays settings (off by "+
				"default). It DELETES. Do not enable against a /data another "+
				"MikroDash owns.")
		// ── THE WEBSOCKET ORIGIN ALLOW-LIST ──────────────────────────────
		//
		// `coder/websocket` accepts a handshake only when the browser's Origin
		// host equals this process's Host header, or matches one of these
		// patterns. Behind a reverse proxy those two are DIFFERENT BY
		// DEFINITION — the browser says `dash.example.com`, the proxy forwards
		// to `192.168.1.10:3081` — so every proxied install was refused with
		// "request Origin ... is not authorized for Host ...". Reported as
		// issue #128, and true of every published Go image because nothing ever
		// set this option (see internal/server.Options.OriginPatterns).
		//
		// EMPTY STAYS SAME-ORIGIN ONLY. That is the right default: this check is
		// what stops a hostile page opening an authenticated socket to a
		// MikroDash the victim is signed in to, so it is opened deliberately
		// and never inferred. In particular `X-Forwarded-Host` is NOT trusted —
		// any client can send it, which would make the check decorative.
		//
		// Patterns are host[:port], matched against the Origin's host, and may
		// wildcard: `dash.example.com`, `192.168.10.45:5081`, `*.example.com`.
		// A non-default port is part of the Origin and must be written.
		//
		// Read from MIKRODASH_ORIGINS when the flag is absent, because the
		// people this strands are running a NAS or unraid UI where an
		// environment variable is reachable and a command array often is not.
		origins = flag.String("origins", os.Getenv("MIKRODASH_ORIGINS"),
			"comma-separated Origin hosts allowed to open a WebSocket, for "+
				"reverse-proxied installs (e.g. dash.example.com,10.0.0.5:8443). "+
				"Empty means same-origin only. Also read from MIKRODASH_ORIGINS")
		// The proxies whose X-Forwarded-For is believed: IPs or CIDR ranges.
		// Issue #111. A DEPLOYMENT FACT, so a flag and an environment variable
		// rather than a Settings field: an admin session, which over a public
		// tunnel is exactly the one to worry about, must not be able to widen
		// who may claim to be any client. Empty trusts nobody, which is right for
		// a direct install.
		trustedProxiesFlag = flag.String("trusted-proxies", os.Getenv("MIKRODASH_TRUSTED_PROXIES"),
			"comma-separated IPs or CIDR ranges of reverse proxies whose X-Forwarded-For "+
				"is believed (e.g. 172.18.0.0/16). Empty trusts none. Also read from "+
				"MIKRODASH_TRUSTED_PROXIES")
		geoDir = flag.String("geo", "/app/geo",
			"geoip-lite's data directory, read for country lookups")
	)
	flag.Parse()

	// A VALUE FOR THE RETIRED -node IS REFUSED, loudly: whoever passed one
	// expects a proxy that no longer exists.
	if strings.TrimSpace(*node) != "" {
		log.Fatalf("[mikrodash] -node %q: the Node coexistence mode was removed; "+
			"remove the flag or leave it empty", *node)
	}

	// Geo is loaded ONCE, here, and its absence is a degraded state rather than
	// a fatal one — a dashboard with no country flags still shows every rate,
	// name and connection. Saying it once at startup is the live app's choice
	// too, and for the same reason: the alternative was three call sites
	// swallowing the failure and every lookup silently returning nothing.
	if _, ok := geo.Shared(*geoDir); !ok {
		log.Printf("[mikrodash] geo lookups unavailable, countries will be empty: %v", geo.Reason())
	}
	// The ASN database is loaded here for the same reasons and on the same
	// terms — one load point, one place the failure is said, every caller
	// gating on availability. It lives beside the city database because it is
	// the same vendor, the same licence and the same `-geo` directory.
	if _, ok := asn.Shared(*geoDir); !ok {
		log.Printf("[mikrodash] organisation lookups unavailable, connection owners "+
			"will be empty: %v", asn.Reason())
	}

	st, err := store.Open(*data)
	if err != nil {
		log.Fatalf("cannot open %s: %v", *data, err)
	}

	// The audit trail is NOT fatal to open. A /data whose database this build
	// cannot read is a real situation — an older schema, a half-migrated
	// deployment — and refusing to serve any page because the trail is
	// unavailable would be a worse outcome than serving them with the trail
	// missing. It is said once, loudly, here rather than per event.
	adb, err := db.Open(*data)
	if err != nil {
		log.Printf("[mikrodash] WARNING: audit trail unavailable, writes will NOT be recorded: %v", err)
		adb = nil
	} else {
		defer adb.Close()
		if v, verr := adb.SchemaVersion(); verr == nil {
			log.Printf("[mikrodash] audit trail open (schema v%d)", v)
		}
		// ── THE SCHEMA STEPS THIS PORT OWNS ──────────────────────────────
		//
		// Versions 16 and up: tables the Node app never had. Here rather than in
		// `db.Open` for the reason `cmd/compat` exists — it opens a production
		// /data read-only, and a migration in Open would fail on the one tool
		// whose job is to touch nothing.
		//
		// NOT FATAL. A /data this cannot migrate still serves every page; what
		// stops working is whatever the new tables back, and those say so.
		if n, merr := adb.Migrate(); merr != nil {
			log.Printf("[mikrodash] WARNING: schema migration failed, some features "+
				"will be unavailable: %v", merr)
		} else if n > 0 {
			log.Printf("[mikrodash] applied %d schema migration(s)", n)
		}

		// A Config Management run the previous process left open is closed, and
		// never resumed: its secret values died with that process, and its
		// confirmation was for then. See db.InterruptCfgRuns.
		if n, ierr := adb.InterruptCfgRuns(); ierr != nil {
			log.Printf("[mikrodash] WARNING: could not close interrupted config runs: %v", ierr)
		} else if n > 0 {
			log.Printf("[mikrodash] marked %d config deploy run(s) interrupted by the restart", n)
		}

		// Page keys are also permission keys, so renaming one strands every
		// grant naming the old one -- silently, and invisibly to the
		// administrator most likely to be looking, because administrators are
		// structural and never consult the table. Not fatal: a database that
		// refuses this is still perfectly able to serve the app.
		if n, rerr := adb.RenamePageGrants(); rerr != nil {
			log.Printf("[mikrodash] WARNING: could not update renamed page grants: %v", rerr)
		} else if n > 0 {
			log.Printf("[mikrodash] moved %d page grant(s) onto renamed pages", n)
		}

		// ── TOMBSTONES FROM AN OLDER BUILD ────────────────────────────────
		//
		// Retention used to MARK a pruned row and keep it for ever; it deletes
		// now. Without this, rows an earlier version marked would sit in the
		// History table indefinitely, labelled "Pruned" and describing files
		// that have long since gone. One-off and idempotent: nothing sets
		// `pruned_at` any more, so after the first start there is nothing to
		// find. Not fatal for the same reason as the rename above.
		if n, perr := adb.PurgePrunedBackups(); perr != nil {
			log.Printf("[mikrodash] WARNING: could not clear pruned backup rows: %v", perr)
		} else if n > 0 {
			log.Printf("[mikrodash] cleared %d pruned backup row(s) an older build left behind", n)
		}
	}

	// ── PER-ROUTER REPORTING: ANSWER ONCE FOR AN UPGRADING INSTALL ────────
	//
	// Before the toggle existed, exactly one router recorded history — the
	// active one. This writes that fact down for records that predate the flag,
	// so an upgrade changes nothing: on for the router that is recording, off
	// for the rest. Idempotent, so after the first start it writes nothing.
	//
	// HERE RATHER THAN IN `store.Open`, and the reason is `cmd/compat`: it opens
	// a real /data through a READ-ONLY mount, and a migration in Open would fail
	// on the one tool whose job is to read a production directory untouched.
	// Same placement, and the same reason, as `RenamePageGrants` above.
	//
	// NOT FATAL. A /data this cannot rewrite is still perfectly able to serve
	// every page; the consequence is that reporting stays off until the operator
	// sets it, which is visible in the UI rather than silent.
	if n, merr := st.MigrateReportingDefaults(st.ActiveRouterID()); merr != nil {
		log.Printf("[mikrodash] WARNING: could not set reporting defaults: %v", merr)
	} else if n > 0 {
		log.Printf("[mikrodash] set the reporting default on %d router(s)", n)
	}

	// The command-rate instrument for the collector rewrite's phase 1. A no-op
	// unless MIKRODASH_CMD_STATS is set, so this costs a nil check on every
	// install that is not being measured.
	roslimit.StartStats(time.Minute)

	// REFUSED AT START, not ignored: a typo here would otherwise leave every
	// visitor behind the proxy sharing one rate limit and one audit address,
	// with nothing saying why.
	trustedProxies, err := trustedproxy.Parse(*trustedProxiesFlag)
	if err != nil {
		log.Fatalf("mikrodash: -trusted-proxies: %v", err)
	}

	srv, err := server.New(st, server.Options{
		WebDir:          *webDir,
		StaticDir:       *staticDir,
		GeoDir:          *geoDir,
		AuditDB:         adb,
		NoPool:          *noPool,
		AlertDispatch:   *alertDispatch,
		BackupScheduler: *backupSched,
		Retention:       *pruneOn,
		History:         *historyOn,
		OriginPatterns:  splitOrigins(*origins),
		TrustedProxies:  trustedProxies,
		// A restore has the ROUTER fetch from us, so the URL it is handed must
		// name this process's port.
		ListenAddr: *listen,
	})
	if err != nil {
		log.Fatalf("cannot build the server: %v", err)
	}

	// ── THE FOUR FIXED TRANSPORTS BECOME CHANNELS, ONCE ──────────────────
	//
	// Delivery reads channels now. An install with Telegram and SMTP working
	// would otherwise stop notifying on upgrade — silently, which is the worst
	// failure available to the feature whose job is to say when something
	// stopped. Runs only on an install with no channels at all.
	//
	// Here rather than in `db.Open` for the reason `RenamePageGrants` gives,
	// and after `server.New` because only the server can decrypt a stored
	// credential and seal a new one. NOT FATAL: an install this cannot write is
	// still able to serve every page, and the operator sees an empty channel
	// list rather than a dead app.
	if n, nerr := srv.SeedNotifyChannels(); nerr != nil {
		log.Printf("[mikrodash] WARNING: could not carry notification settings into channels: %v",
			nerr)
	} else if n > 0 {
		log.Printf("[mikrodash] carried %d notification destination(s) into channels", n)
	}

	// AFTER the channel seeding above, and the order is load-bearing: this one
	// copies the install's mail SERVER onto each channel it makes, and on an
	// upgrade that server only becomes a channel in the step above.
	//
	// Same placement rule, and the same reason: it needs the Server to seal a
	// credential, and `cmd/compat` opens a real /data read-only and never builds
	// one. Not fatal either — an install whose lists cannot be carried keeps its
	// schedules and its recipients, and tries again next start.
	if n, rerr := srv.SeedReportChannels(); rerr != nil {
		log.Printf("[mikrodash] WARNING: could not carry report recipients into channels: %v",
			rerr)
	} else if n > 0 {
		log.Printf("[mikrodash] carried report recipients into %d channel(s)", n)
	}

	// AFTER both seeders, so every channel that is going to exist exists before
	// the install's thresholds are written onto it. A channel created by either
	// step above arrives untuned and is picked up here.
	if n, terr := srv.SeedChannelTuning(); terr != nil {
		log.Printf("[mikrodash] WARNING: could not carry alert thresholds onto channels: %v",
			terr)
	} else if n > 0 {
		log.Printf("[mikrodash] carried alert thresholds onto %d channel(s)", n)
	}

	hs := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// No WriteTimeout: it would cut long-lived WebSockets. ReadHeaderTimeout
		// is the one that actually protects against a slow-header attack.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("[mikrodash] serving %s", *listen)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("[mikrodash] shutting down")

	// ── A FORCED EXIT, WHICH THE LIVE APP HAS AND THIS DID NOT ────────────
	//
	// `src/shutdown.js` is one function and exists for one reason: if the
	// graceful path hangs, exit anyway. `hs.Shutdown` respects its context, but
	// `srv.Shutdown` closes router sockets and a database, and a blocked socket
	// close has no deadline. Without this the process sits until Docker's own
	// timeout escalates to SIGKILL — which is the outcome the graceful path
	// exists to avoid, reached the slow way.
	//
	// The timer starts BEFORE the graceful work rather than after, so the budget
	// covers all of it. Exit code 1, as the live one uses: a shutdown that had to
	// be forced is not a clean one, and an orchestrator should be able to tell.
	forced := time.AfterFunc(forcedShutdownAfter, func() {
		log.Printf("[mikrodash] forceful shutdown after %s", forcedShutdownAfter)
		os.Exit(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
	srv.Shutdown()
	forced.Stop()
	log.Print("[mikrodash] stopped")
}

// forcedShutdownAfter is the live `scheduleForcedShutdownTimer`'s 5000ms, plus
// the HTTP server's own 5s budget — this one bounds BOTH halves, so it must be
// longer than the half it contains or it would fire during a normal shutdown.
const forcedShutdownAfter = 10 * time.Second

// splitOrigins turns the flag's comma list into patterns.
//
// Blanks are dropped and each entry is trimmed, so `a, b,` is two patterns
// rather than four — a trailing comma in a compose file or a NAS text box must
// not produce an empty pattern, because an empty one matches nothing and would
// look like the setting was ignored.
func splitOrigins(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
