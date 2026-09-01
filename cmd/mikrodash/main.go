// Command mikrodash is the Go half of the port, running in front of the Node
// app and taking over one page at a time.
//
// It is deliberately a SEPARATE listener rather than a replacement: the live
// app keeps serving on its own port, this one proxies to it, and a ported page
// can be compared against the original in the same browser with the same
// session. Nothing about the existing deployment changes to run this.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/geo"
	"mikrodash/internal/server"
	"mikrodash/internal/store"
)

func main() {
	var (
		listen = flag.String("listen", ":3082", "address to serve on")
		// ── EMPTY BY DEFAULT: THIS PROCESS IS THE APP ─────────────────────
		//
		// This defaulted to `http://127.0.0.1:3081` — the strangler-fig default,
		// correct while Node owned that port and this process sat in front of
		// it. At CUTOVER that default becomes a loop: the app binds :3081 and
		// proxies everything it has not ported TO ITSELF.
		//
		// MEASURED, on the cutover itself (2026-08-30). The first attempt logged
		// "serving :3081, proxying the rest to http://127.0.0.1:3081", and with
		// it the pool and the retention sweep silently stayed OFF, because both
		// are gated on `standalone` and standalone means "no -node". Three
		// subsystems wrong from one stale default, and the only tell was one
		// line in a startup banner nobody had a reason to re-read.
		//
		// Standalone is now the default and proxying is the opt-in, which is the
		// right way round once the port IS the app. `mustNotProxyToSelf` below
		// makes the loop unrepresentable rather than merely unlikely.
		node = flag.String("node", "", "proxy un-ported routes to this Node MikroDash "+
			"(empty = standalone, this process is the whole app)")
		staticDir = flag.String("static", "web/public",
			"the vendored asset tree (/vendor, /css, /fonts, /logo.png, the login page). "+
				"Empty falls back to proxying them from Node, which is what happened before "+
				"they were vendored on 2026-08-27")
		data    = flag.String("data", "/data", "the MikroDash data directory, read as Node wrote it")
		webDir  = flag.String("web", "web/dist", "the built frontend")
		authTTL = flag.Duration("auth-ttl", 15*time.Second, "how long a validated session may be cached")
		// The background pool runs by default in standalone, because standalone
		// normally means nothing else is polling this fleet. Running this beside
		// the live app to compare the two breaks that implication — both pools
		// then hold a connection to every unwatched router, and API channels are
		// the documented bottleneck. This is the way out.
		noPool = flag.Bool("no-pool", false,
			"do not run the background router pool (use when another MikroDash is polling the same fleet)")
		// OFF BY DEFAULT, and deliberately so. The alert EVALUATOR always runs
		// and writes its rows; this switch controls only whether a notification
		// is SENT. While the Node app is also running, both engines watch the
		// same routers with in-memory cooldowns neither can see — and a
		// duplicated Telegram message or email cannot be un-received. After
		// cutover, pass it.
		alertDispatch = flag.Bool("alert-dispatch", false,
			"SEND alert notifications (off by default; the evaluator and its database "+
				"rows run either way). Do not enable while another MikroDash watches the "+
				"same routers: both would send.")
		// OFF BY DEFAULT, like -alert-dispatch and for the same reason: during
		// coexistence Node is already taking these backups. Two schedulers means
		// two restore points per router per schedule, each holding a router
		// channel while it runs.
		backupSched = flag.Bool("backup-scheduler", false,
			"take SCHEDULED backups (off by default). Do not enable while another "+
				"MikroDash is backing up the same routers.")
		// OFF BY DEFAULT, like the two above. Two processes bucketing the same
		// per-second samples write two rows per minute per interface, and
		// Reports averages by minute — a plausible chart with wrong numbers.
		historyOn = flag.Bool("history", false,
			"record traffic, ping and connectivity history (off by default). Do not "+
				"enable while another MikroDash is recording the same routers.")
		// OFF BY DEFAULT, like the three above, AND FOR A SHARPER REASON: it is
		// the only one of the four that DELETES.
		//
		// It was gated on `standalone` alone until 2026-08-29, on the argument
		// that unlike its siblings it does not act on the fleet — it only ages
		// rows out of a database Node prunes identically when Node is there. That
		// argument is right for a cutover deployment and wrong for the routine
		// activity this repo does constantly: `tools/live-diff.sh` stands a Go
		// server up against the LIVE /data, and `standalone` is nothing more than
		// "no -node was passed". A dry run with the flag omitted would have
		// pruned the production database unattended, irreversibly.
		//
		// Nothing was lost — that script passes `-node`, so it was never
		// standalone — but the safety came from an unrelated default rather than
		// from a decision. So retention joins the other three: a flag, off by
		// default, named in the cutover checklist.
		pruneOn = flag.Bool("retention", false,
			"age rows out of the database per the dbRetentionDays settings (off by "+
				"default). It DELETES. Do not enable against a /data another "+
				"MikroDash owns.")
		geoDir = flag.String("geo", "/app/geo",
			"geoip-lite's data directory, read for country lookups")
	)
	flag.Parse()

	// A PROXY TARGET THAT IS OUR OWN LISTEN ADDRESS IS A LOOP, and refusing is
	// the only safe answer: every un-ported route would recurse into this
	// process until something ran out. Fatal rather than a warning, because the
	// startup banner already said this once and it was read past.
	if err := mustNotProxyToSelf(*listen, *node); err != nil {
		log.Fatalf("[mikrodash] %v", err)
	}

	// Geo is loaded ONCE, here, and its absence is a degraded state rather than
	// a fatal one — a dashboard with no country flags still shows every rate,
	// name and connection. Saying it once at startup is the live app's choice
	// too, and for the same reason: the alternative was three call sites
	// swallowing the failure and every lookup silently returning nothing.
	if _, ok := geo.Shared(*geoDir); !ok {
		log.Printf("[mikrodash] geo lookups unavailable, countries will be empty: %v", geo.Reason())
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
	}

	srv, err := server.New(st, server.Options{
		NodeURL:         *node,
		WebDir:          *webDir,
		StaticDir:       *staticDir,
		GeoDir:          *geoDir,
		AuthTTL:         *authTTL,
		AuditDB:         adb,
		NoPool:          *noPool,
		AlertDispatch:   *alertDispatch,
		BackupScheduler: *backupSched,
		Retention:       *pruneOn,
		History:         *historyOn,
		// A restore has the ROUTER fetch from us, so the URL it is handed must
		// name this process's port.
		ListenAddr: *listen,
	})
	if err != nil {
		log.Fatalf("cannot build the server: %v", err)
	}

	hs := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// No WriteTimeout: it would cut long-lived WebSockets. ReadHeaderTimeout
		// is the one that actually protects against a slow-header attack.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		// `-node` NAMES A PROXY TARGET AND DEFAULTS TO EMPTY, which is the only
		// configuration that exists now. The branch is kept because the flag is:
		// pointing it somewhere still proxies, and a banner that did not say so
		// would be the one line an operator reads to confirm the mode, printing
		// a comfortable lie.
		//
		// It once printed "proxying the rest to " with an empty target, which is
		// how an empty flag came to look like a configured one.
		if *node == "" {
			log.Printf("[mikrodash] serving %s", *listen)
		} else {
			log.Printf("[mikrodash] serving %s, proxying the rest to %s", *listen, *node)
		}
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

// mustNotProxyToSelf refuses a `-node` that points back at our own listener.
//
// Compares HOST AND PORT, not the raw strings: `-listen :3081` and
// `-node http://127.0.0.1:3081` are the same endpoint written two ways, and a
// string compare would have missed exactly the case that happened.
//
// An empty `-node` is standalone and always fine. A `-listen` with no host means
// every interface, so any loopback or unspecified host on the same port is us.
func mustNotProxyToSelf(listen, node string) error {
	if strings.TrimSpace(node) == "" {
		return nil
	}
	u, err := url.Parse(node)
	if err != nil {
		return fmt.Errorf("-node %q is not a URL: %w", node, err)
	}
	nodePort := u.Port()
	if nodePort == "" {
		if u.Scheme == "https" {
			nodePort = "443"
		} else {
			nodePort = "80"
		}
	}
	_, listenPort, err := net.SplitHostPort(listen)
	if err != nil || listenPort == "" || listenPort != nodePort {
		return nil // different port, so it cannot be us
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0", "":
		return fmt.Errorf("-node %s points at this process's own listener (%s): "+
			"every un-ported route would proxy to itself. Pass -node= for standalone, "+
			"or give -node the address of a DIFFERENT MikroDash", node, listen)
	}
	return nil
}
