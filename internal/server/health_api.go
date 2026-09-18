package server

import (
	"net/http"
	"time"
)

// `GET /healthz` — the port of `src/health.js` plus its route.
//
// ── IT WAS MISSING, AND TWO THINGS DEPENDED ON IT ─────────────────────────
//
//	the container   `docker-compose.yml` runs
//	                `wget -qO- http://127.0.0.1:3081/healthz` as its HEALTHCHECK.
//	                A port that 404s here comes up permanently unhealthy.
//	the app itself  `web/src/pages/settings.ts` and `web/src/account.ts` both
//	                fetch it for the version string. Those were failing silently
//	                — each guards on `d.version`, so a 404 shows a blank instead
//	                of an error.
//
// Found on 2026-08-29 by listing live's modules and asking which have no port
// equivalent. The endpoint audit could not have found it twice over: it
// only looked at `/api` paths, and its wildcard matcher treated Go's `{$}`
// end-of-path anchor as a segment wildcard, so `/{$}` "served" every
// single-segment path. Both are fixed.
//
// ── WHAT MAKES IT OK ──────────────────────────────────────────────────────
//
// `computeHealthStatus` is `startupReady && rosConnected && nothing stale`. The
// port has no per-collector freshness ledger, so the third clause has no
// equivalent yet and is NOT faked: reporting healthy on two of three checks is
// honest; inventing a `stale` array that is always empty would look like the
// third check passing.
func (s *Server) registerHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	connected, activeID := s.activeRouterHealth()
	// ── NOTHING TO CONNECT TO IS NOT THE SAME AS FAILING TO CONNECT ───────
	//
	// `ok` was simply `connected`, and `activeRouterHealth` reports false when
	// no device is configured — so an install nobody had set up yet answered 503
	// for ever. Not even `starting`: past the grace window it was flatly
	// unhealthy, which is the state a fresh container is SUPPOSED to be in.
	//
	// THAT DEADLOCKED THE ROUTEROS APP INSTALL (issue #120,
	// `docs/routeros-container-install.md`): the App withholds its UI-URL until
	// the container reports healthy, the container was unhealthy until a device
	// existed, and a device can only be added through that UI. The reporter got
	// in by typing the address by hand, which is not something a first-time user
	// knows to do.
	//
	// So there are THREE states here, not two:
	//
	//	no device configured    healthy — there is nothing to be disconnected from
	//	device, not answered    starting, inside the grace window
	//	device, still silent    unhealthy, and an orchestrator should act
	//
	// Only the first is new. `deviceExpected` is what separates it from the
	// other two, and it is deliberately strict about not knowing: a fleet file
	// it cannot read counts as "a device is expected", so a corrupt
	// `routers.json` still reports unhealthy rather than being mistaken for a
	// fresh install.
	expected := s.deviceExpected()
	ok := connected || !expected

	// STARTING is not FAILING. The live route distinguishes them so an
	// orchestrator does not kill a container that is still dialling: a 503
	// during the grace window is expected, and the body says which it is.
	//
	// Keyed off `ok` rather than `connected`, so an install with no device is
	// READY rather than perpetually on its way to something.
	starting := !ok && time.Since(s.startedAt) < healthStartupGrace

	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}

	// ── AN UNAUTHENTICATED CALLER GETS THE STATUS AND NOTHING ELSE ────────
	//
	// The live comment: "version, router ids and collector detail would
	// otherwise be free fingerprinting for anyone who can reach the port." The
	// Docker healthcheck needs only the code and these two flags.
	if _, err := s.auth.Validate(r.Header.Get("Cookie")); err != nil {
		w.WriteHeader(code)
		writeJSON(w, map[string]any{"ok": ok, "starting": starting})
		return
	}

	w.WriteHeader(code)
	writeJSON(w, map[string]any{
		"ok": ok, "starting": starting,
		"routerConnected": connected,
		"activeRouterId":  activeID,
		// WHY it is ok, for a signed-in caller. `ok:true` with
		// `routerConnected:false` is otherwise indistinguishable from a bug, and
		// this is the field that says "because there is no device to connect
		// to". Authenticated only, like everything below it.
		"deviceConfigured": expected,
		"startupReady":     !starting,
		"uptime":           time.Since(s.startedAt).Seconds(),
		"now":              time.Now().UnixMilli(),
		"version":          AppVersion,
		// NO `checks` MAP, and that omission IS deliberate: `computeHealthStatus`
		// builds it from a per-collector freshness ledger this port does not
		// have. Reporting healthy on two of three checks is honest; an always-
		// empty `stale` array would look like the third one passing.
	})
}

// AppVersion is what this build reports as the application version.
//
// ── SET TO MATCH THE LIVE APP, ON THE OPERATOR'S INSTRUCTION (2026-08-29) ──
//
// Reported as `version` on /healthz; `web/src/pages/settings.ts` and
// `web/src/account.ts` render `'v' + d.version`, so this is the bare number with
// no `v`.
//
// It is a CONSTANT rather than read from a file. The Node app read it from
// package.json, which no longer exists, and inventing a file to hold one number
// would add a build input for nothing. CLAUDE.md's rule still applies: a bump
// happens only when the operator says package it up, and one bump covers the
// whole session.
//
// 0.8.0 was the cutover release — the first on Go and TypeScript — but it never
// produced an image: its build failed on the newly restored 32-bit ARM target.
// 0.8.1 is that same cutover with the 32-bit fix, and was the first published
// Go image. 0.7.40 was the last on Node.
//
// 0.8.10 SORTS AFTER 0.8.2, and the jump is deliberate rather than a typo: these
// are numbers, not decimals, so ten follows two. Docker tags are strings and
// sort lexically, which is why the next one is 0.8.11 and not 0.8.3.
//
// 0.8.11 is the first release a NEW install can complete at all: until it, a
// fresh /data had no `.secret` (so the process exited before serving a page) and
// no database (so the first administrator held no grants). See issue #124.
//
// 0.8.12 finishes that job. 0.8.11 created the files and STILL could not be set
// up: a missing users.json was reported as a read error rather than as "no users
// yet", so `firstRun` never went true and the login page offered a Sign In form
// for an account that could not exist. Same issue, one layer up.
//
// 0.8.13 is the LAST step of that same walk, found by the same reporter getting
// one screen further each time: the wizard worked, and then Add Device did
// nothing, because `#rtrAddBtn` had no listener bound to it anywhere. A new
// install could create its administrator and still not add a router. Three
// releases to make a first run work end to end is worth remembering when the
// next port lands.
//
// 0.8.14 is the one after that, and it stops the walk: a new install now lands
// on the router wizard instead of an empty dashboard. The overlay had existed
// all along and was shown on ONE trigger — the last router being deleted — so it
// could never appear on a first run, which is the only case it is for.
//
// 0.8.15 is the one where the Settings page can save. `#settingsSaveBtn` was
// bound to nothing at all, so no server-side setting could be written from any
// tab — the third control found unwired in a week, after the Add Device button
// and the first-run wizard. `TestInteractiveControlsAreBoundBeyondCaps` is the
// gate that would have caught all three.
//
// 0.8.16 fixes the THIRD instance of one class in a week: a config file that
// does not exist yet reported as a failure. users.json in 0.8.12, routers.json
// in 0.8.14, settings.json here — where it stopped a clean install activating
// its first router. `readIfPresent` is the rule in one place, and
// `TestEveryConfigReaderSurvivesAFreshInstall` is what stops a fourth.
//
// ONE DEFINITION. Anything else needing the app version reads this.
const AppVersion = "0.8.60"

// healthStartupGrace matches the live `STARTUP_GRACE_MS`: a container that has
// not finished its first dial is starting, not broken.
const healthStartupGrace = 90 * time.Second

// deviceExpected reports whether this install has been given a device to
// connect to yet — and therefore whether "not connected" is a fault or just an
// install nobody has set up.
//
// ── NOT KNOWING COUNTS AS EXPECTING ONE ────────────────────────────────────
//
// Three answers, and the middle one is why this is not a length check inline at
// the call site:
//
//	routers on file     a device is expected; silence is a fault
//	none, read cleanly  no device is expected; silence is the correct state
//	cannot be read      a device is EXPECTED, because a fleet file that will not
//	                    parse is a real problem and must not be mistaken for a
//	                    fresh install
//
// The third is the one that keeps this honest. `store.Routers` returns no
// routers both when there are none and when the file failed to decode — one
// stray `"disabled": "false"` is enough, as its own comment records — so reading
// the count alone would report a broken install as a brand new one, which is
// exactly the confusion this whole change is about.
func (s *Server) deviceExpected() bool {
	if s.store == nil {
		// No store wired at all. Not a state a built server reaches, and "cannot
		// tell" takes the strict branch for the reason above.
		return true
	}
	all, errs := s.store.Routers()
	if len(all) > 0 {
		return true
	}
	return len(errs) > 0
}

// activeRouterHealth reports whether the router this install is pointed at is
// reachable, and which one that is.
//
// It asks the INTERACTIVE session first and the always-on pool second, because
// those are the two things that hold a connection — and after the fleet holds
// landed, a router nobody is watching is genuinely connected rather than merely
// unknown. Before that this would have read "down" for the whole fleet whenever
// nobody had a browser open, which is exactly the wrong answer for a healthcheck.
func (s *Server) activeRouterHealth() (bool, string) {
	activeID := ""
	if cfg, err := s.mergedSettings(); err == nil {
		activeID, _ = cfg["activeRouterId"].(string)
	}
	if activeID == "" {
		return false, ""
	}
	if s.sessions != nil {
		if live := s.sessions.Live(); live[activeID] != nil {
			return live[activeID].Connected(), activeID
		}
	}
	if s.sessions != nil {
		if up, known := s.sessions.Status()[activeID]; known {
			return up, activeID
		}
	}
	// ── AND THE OVERVIEW POOL, THE THIRD HOLDER ───────────────────────────
	//
	// This asked the two sources it knew about and then gave up, reporting the
	// active router DISCONNECTED whenever the component actually holding it was
	// the third one.
	//
	// That is not a rare state. `warmExclusions` removes from the alert
	// pool every router the overview pool has ANSWERED for — deliberately, so
	// one router is never held by both — and the manager forgets the status
	// of a router it drops. So while anybody has the Devices page open, the
	// manager has no entry for the active router and this returned false for
	// a router that was up and being watched.
	//
	// Worse, it never recovered after a router edit: `routerUpdate` calls
	// `syncPool`, which dials the whole fleet, and nothing schedules a release
	// because nobody was watching the Devices page to stop watching it. Measured
	// against the shipped 0.8.18: `/healthz` went to `ok:false` after one edit
	// and stayed there.
	//
	// `/healthz` answering 503 for a healthy install is not cosmetic — it is how
	// an orchestrator decides to restart the container.
	//
	// KNOWN IS THE GATE, as it is in `warmExclusions` and on the Devices
	// page: a summary exists as soon as `Sync` builds the session, so
	// `Connected: false` is the zero value until the first dial returns. Reading
	// it before then would report a router as down for the second it takes to
	// answer.
	if s.pool != nil {
		for _, sum := range s.pool.Summaries() {
			if sum.RouterID == activeID && sum.Known {
				return sum.Connected, activeID
			}
		}
	}
	return false, activeID
}
