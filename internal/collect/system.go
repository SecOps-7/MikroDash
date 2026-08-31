package collect

// System collector.
//
//	/system/resource             the gauges: cpu, memory, disk, uptime, version
//	/system/health               temperature, on the boards that report one
//	/system/routerboard          serial number
//	/system/license              licence level
//	/system/package/update       what the last update check found
//
// FOUR MENUS ON FOUR DIFFERENT CADENCES, and the differences are the design.
// The gauges are read every tick; health changes slowly and is read every
// thirty seconds; the serial and the licence level cannot change at all while
// the router is up, so they are read ONCE; and the update check leaves the
// router entirely — it reaches upgrade.mikrotik.com — so it runs on a twelve
// hour schedule of its own and never on the tick path.
//
// THE FIRST PAYLOAD CARRIES NO SERIAL, AND THAT IS THE LIVE BEHAVIOUR. The Node
// collector calls _fetchStaticInfo() fire-and-forget from _processRow and then
// builds the payload from fields that call has not filled yet, so the serial and
// the licence level appear on the SECOND emit, not the first. Reproduced here by
// reading them at the start of the next tick rather than by racing a goroutine:
// same two-emit sequence, deterministic, and one fewer concurrent channel.
//
// The Node original streams /system/resource with `=interval=N`. This side polls
// it instead — same rows, one fewer channel held open, which is what CLAUDE.md
// means by more efficient. The payload is identical either way.

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// Declared as Cmd values rather than inline so the proplist drift gate can
// compare them against what system.js asks for.
var (
	systemResourceCmd = routeros.Cmd{Path: "/system/resource/print", Args: []string{
		"=.proplist=cpu-load,total-memory,free-memory,total-hdd-space,free-hdd-space," +
			"version,board-name,platform,cpu-count,cpu-frequency,uptime,architecture-name"}}
	systemHealthCmd      = routeros.Cmd{Path: "/system/health/print"}
	systemRouterboardCmd = routeros.Cmd{Path: "/system/routerboard/print"}
	systemLicenseCmd     = routeros.Cmd{Path: "/system/license/print"}
	systemUpdateCheckCmd = routeros.Cmd{Path: "/system/package/update/check-for-updates"}
	systemUpdatePrintCmd = routeros.Cmd{Path: "/system/package/update/print"}
)

const (
	// Health is polled on its own timer, not every tick: it changes slowly and
	// the menu does not support interval streaming.
	systemHealthEvery = 30 * time.Second
	// The update check reaches MikroTik's servers, so it is rate limited hard.
	systemUpdateInterval   = 12 * time.Hour
	systemUpdateRetry      = 60 * time.Second
	systemUpdateMaxRetries = 3
	// check-for-updates blocks until the update server answers or the router
	// gives up; /print is local and answers at once.
	systemCheckTimeout = 15 * time.Second
)

// SystemPayload is what the dashboard's gauges and the Updates card read.
type SystemPayload struct {
	TS        int64  `json:"ts"`
	UptimeRaw string `json:"uptimeRaw"`
	CPULoad   int    `json:"cpuLoad"`
	MemPct    int    `json:"memPct"`
	UsedMem   int    `json:"usedMem"`
	TotalMem  int    `json:"totalMem"`
	HddPct    int    `json:"hddPct"`
	TotalHdd  int    `json:"totalHdd"`
	FreeHdd   int    `json:"freeHdd"`
	Version   string `json:"version"`

	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	UpdateStatus    string `json:"updateStatus"`
	// UpdateChannel comes back in the same `/system/package/update` row as the
	// other three. The upgrade dialog has always had a line for it and read it
	// off the payload, which nothing ever set — so it rendered blank on every
	// router until the live collector started sending it. It is the one thing
	// distinguishing a stable upgrade from a testing one.
	UpdateChannel string `json:"updateChannel"`

	BoardName string   `json:"boardName"`
	CPUCount  int      `json:"cpuCount"`
	CPUFreq   int      `json:"cpuFreq"`
	TempC     *float64 `json:"tempC"`
	PollMs    int      `json:"pollMs"`

	// Three fields the page renders as an em dash when absent, so they are
	// pointers rather than empty strings: "not read yet" and "this router has
	// no routerboard" both have to render as nothing, and `""` would render as
	// nothing while claiming to be an answer.
	Arch         *string `json:"arch"`
	Serial       *string `json:"serial"`
	LicenseLevel *string `json:"licenseLevel"`
}

// tempFromHealth is the original's scan: the FIRST health row whose name
// contains "temperature" and whose value parses. A board reporting both a CPU
// and a board temperature therefore reports the first one the router listed,
// which is what the live gauge shows.
func tempFromHealth(rows []routeros.Reply) *float64 {
	for _, row := range rows {
		if !strings.Contains(strings.ToLower(row["name"]), "temperature") {
			continue
		}
		if v, ok := parseJSNumber(row["value"]); ok {
			return &v
		}
	}
	return nil
}

// updateVerdict is the same reading of an update row that the Packages page
// makes — deliberately, because the same router state must not produce two
// different answers on two pages.
//
// `latest-version` decides when the router has one. Otherwise the STATUS TEXT
// does, and the string matched is RouterOS's own: MikroTik's upgrade
// documentation scripts against `[/system/package/update get status] = "New
// version is available"`.
func updateVerdict(latest, status, installed string) bool {
	if latest != "" {
		return latest != installed
	}
	return strings.Contains(strings.ToLower(status), "new version")
}

// buildSystem is the whole payload, pure. The arithmetic is the original's,
// including its guards: a zero total gives a zero percentage rather than a
// division by zero, and the used figure is a subtraction rather than a
// separately reported field, because RouterOS reports free memory and not used.
//
// The parses are `parseInt(x || '0', 10)`. A non-numeric value would be NaN in
// the original and null in its JSON; this menu reports numbers for every one of
// these fields on every RouterOS 7 build, so that path is not modelled.
func buildSystem(r routeros.Reply, health []routeros.Reply, update routeros.Reply,
	serial, license *string, pollMs int) *SystemPayload {

	intOf := func(key string) int {
		if v := jsParseInt(r[key]); v != nil {
			return *v
		}
		return 0
	}

	totalMem := intOf("total-memory")
	usedMem := totalMem - intOf("free-memory")
	memPct := 0
	if totalMem > 0 {
		memPct = int(math.Round(float64(usedMem) / float64(totalMem) * 100))
	}

	totalHdd := intOf("total-hdd-space")
	freeHdd := intOf("free-hdd-space")
	hddPct := 0
	if totalHdd > 0 {
		hddPct = int(math.Round(float64(totalHdd-freeHdd) / float64(totalHdd) * 100))
	}

	installed := r["version"]
	// The channel travels in a parenthetical — "7.24 (stable)" — and the update
	// server answers a bare "7.24". Comparing the two as they stand would report
	// an update on every router that reports its channel.
	installedBase := strings.TrimSpace(parenSuffix.ReplaceAllString(installed, ""))
	latest := update["latest-version"]
	status := update["status"]

	// board-name OR platform: a CHR has no board name and answers on platform
	// instead, and a card headed by neither reads as broken.
	board := r["board-name"]
	if board == "" {
		board = r["platform"]
	}

	cpuCount := 1
	if v := jsParseInt(r["cpu-count"]); v != nil {
		cpuCount = *v
	}

	var arch *string
	if a := r["architecture-name"]; a != "" {
		arch = &a
	}

	return &SystemPayload{
		TS:              time.Now().UnixMilli(),
		UptimeRaw:       r["uptime"],
		CPULoad:         intOf("cpu-load"),
		MemPct:          memPct,
		UsedMem:         usedMem,
		TotalMem:        totalMem,
		HddPct:          hddPct,
		TotalHdd:        totalHdd,
		FreeHdd:         freeHdd,
		Version:         installed,
		LatestVersion:   latest,
		UpdateAvailable: updateVerdict(latest, status, installedBase),
		UpdateStatus:    status,
		UpdateChannel:   update["channel"],
		BoardName:       board,
		CPUCount:        cpuCount,
		CPUFreq:         intOf("cpu-frequency"),
		TempC:           tempFromHealth(health),
		PollMs:          pollMs,
		Arch:            arch,
		Serial:          serial,
		LicenseLevel:    license,
	}
}

// System is the collector.
type System struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval

	// mu guards everything the update goroutine touches. It is the only
	// concurrency in this collector, and it exists because the update check can
	// block for fifteen seconds and must not hold up the gauges.
	mu      sync.Mutex
	health  []routeros.Reply
	update  routeros.Reply
	serial  *string
	license *string
	last    *SystemPayload
	lastFp  string

	// onIdentity is called when this router's hardware/firmware identity CHANGES.
	// See SetOnIdentity.
	onIdentity      IdentityFunc
	lastIdentityKey string

	staticRead  bool      // the serial and licence have been read for this connection
	firstTick   bool      // one tick has run, so the static read may happen now
	healthAt    time.Time // when health was last read
	updateAt    time.Time // when the update check last ran
	updateRuns  bool      // a check is in flight
	updateTries int

	loop *pollLoop
}

// NewSystem builds the collector. The bounds are Node's — the gauges are what
// the dashboard animates, so this is the fastest poll in the app.
func NewSystem(ros Reader, emit Emit, pollMs int) *System {
	s := &System{
		ros:    ros,
		emit:   emit,
		pollMs: newPollInterval(clampPoll(pollMs, 2000, 500, 60000)),
		update: routeros.Reply{},
	}
	s.loop = newPollLoop(func() { s.Tick() }, func() time.Duration {
		return s.pollMs.duration()
	})
	return s
}

// Identity is what a router reports about ITSELF, as opposed to how it is
// currently doing. The live shape, field for field:
//
//	this._onIdentity({ model: payload.boardName, serial: payload.serial,
//	                   osVersion: installedBase })
//
// `Model` IS `boardName`, and the name difference is the live app's: the record
// on disk calls it `model` and the payload calls it `boardName`. Keeping the
// record's name here means the writer at the other end does not have to
// translate, which is where a field would get crossed.
type Identity struct {
	Model     string
	Serial    string
	OSVersion string
}

// IdentityFunc receives it.
type IdentityFunc func(Identity)

// SetOnIdentity installs the hook. Call before Start — it is read on the poll
// goroutine and there is no lock around installation, exactly as the live
// assignment to `_onIdentity` happens before `start()`.
//
// ── WHY THIS LIVES ON THE COLLECTOR ─────────────────────────────────────────
//
// The background pool used to call its identity hook ONCE, on connect, from
// `s.system.Last()`. Two things were wrong with that and both are silent:
//
//  1. `Last()` is nil at that moment. The collectors have just been started and
//     no tick has run, so the call was a no-op on every connection — the hook
//     was wired and never fired.
//  2. It could never fire AGAIN. Model and serial are fixed for the life of a
//     device, but the OS version changes on upgrade, and the live comment is
//     explicit that this "must not be write-once".
//
// The live app puts it here for exactly that reason: it runs every tick and
// dedupes on the triple.
func (s *System) SetOnIdentity(fn IdentityFunc) { s.onIdentity = fn }

func (s *System) Suspend() { s.loop.stop() }

func (s *System) Resume() {
	if s.ros.Connected() {
		s.loop.start()
	}
}

// Start begins the gauge poll and kicks the one update check that runs at
// startup. Everything slower than the tick is scheduled from inside Tick, so
// there is one timer here rather than three.
func (s *System) Start() {
	s.loop.start()
	go s.checkForUpdates()
}

func (s *System) Stop() { s.loop.stop() }

// SetPollMs applies a new poll period to a RUNNING collector.
//
// Two halves, and one alone is not enough:
//
//   - the stored period, because 21 of the 24 collectors send it to the browser
//     in their payload and the live ones send the mutated value; and
//   - `retime`, because `delay` is only consulted when the loop next schedules,
//     so without it a change from sixty seconds to five would wait out the
//     remaining fifty-nine first.
//
// The value arrives already clamped by `collection.PollRetunes` (500..600000).
// It is NOT re-clamped to this collector's constructor range: the operator set a
// fleet-wide interval and the live app applies it as given.
func (s *System) SetPollMs(ms int) {
	s.pollMs.set(ms)
	s.loop.retime()
}

// Reconnected drops what cannot survive a new connection and restarts the poll,
// matching every other collector here.
//
// THE UPDATE RESULT AND ITS SCHEDULE DELIBERATELY SURVIVE. Resetting them meant
// every reconnect fired another check-for-updates, so a flapping link turned a
// twelve hour interval into one upstream call per flap — and wiping the row
// blanked the version card until the next check. The serial and the licence do
// NOT survive: the usual reason a connection dropped is an upgrade, and the
// router that came back need not be the same build.
func (s *System) Reconnected() {
	s.loop.stop()
	s.mu.Lock()
	s.staticRead = false
	s.serial, s.license = nil, nil
	s.firstTick = false
	s.healthAt = time.Time{}
	s.lastFp = ""
	s.mu.Unlock()
	s.Tick()
	s.loop.start()
}

func (s *System) Last() *SystemPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Tick reads the gauges, and whatever slower menu is due alongside them.
func (s *System) Tick() {
	if !s.ros.Connected() {
		return
	}

	// The static read happens from the SECOND tick on. See the note at the top:
	// the first payload carries no serial in the live app either.
	s.mu.Lock()
	doStatic := s.firstTick && !s.staticRead
	doHealth := time.Since(s.healthAt) >= systemHealthEvery
	s.mu.Unlock()

	if doStatic {
		s.readStatic()
	}
	if doHealth {
		s.readHealth()
	}

	rows, err := s.ros.Do(systemResourceCmd)
	if err != nil || len(rows) == 0 {
		return
	}

	s.mu.Lock()
	s.firstTick = true
	payload := buildSystem(rows[0], s.health, s.update, s.serial, s.license, s.pollMs.ms())
	s.last = payload
	// The fingerprint is what the ORIGINAL compares, field for field: a gauge
	// that has not moved is not worth a frame. Note what is absent from it —
	// memory and disk totals never change, and the serial cannot.
	fp := systemFingerprint(payload)
	changed := fp != s.lastFp
	s.lastFp = fp
	s.mu.Unlock()

	// ── IDENTITY, OUTSIDE THE EMIT GATE ────────────────────────────────────
	//
	// The live call sits above its own `if (changed)`, and that placement is
	// load-bearing: the fingerprint deliberately EXCLUDES the serial and the
	// memory totals ("a gauge that has not moved is not worth a frame"), so a
	// router whose gauges are steady emits nothing — and an identity gated on
	// `changed` would never be reported on a quiet device.
	//
	// It is deduped on its own triple instead, which is what makes it cheap
	// enough to run every tick.
	s.reportIdentity(payload)

	if changed {
		// ROUTER-WIDE, not a page room: these are the top bar's gauges, the
		// uptime chip and the RouterOS version row. A viewer sees them on every
		// page, so gating them on a page focus would blank the chrome.
		s.emit("", "system:update", payload)
	}
}

// reportIdentity fires the hook when the triple has moved.
//
// ── THE KEY IS THE LIVE ONE, JOIN CHARACTER INCLUDED ────────────────────────
//
//	const identityKey = [payload.boardName, payload.serial, installedBase].join(' ')
//
// A nil serial joins as EMPTY in JavaScript, which is why the first tick and the
// second produce different keys and the hook fires TWICE on a fresh connection:
// the static read that fetches the serial happens from the second tick on, so
// the first report carries no serial and the second adds it. That is not waste —
// it is what gets the serial persisted at all, and the writer's "an empty field
// is skipped, not cleared" rule is what makes the first one harmless.
//
// ── installedBase, NOT payload.Version ──────────────────────────────────────
//
// The live comment: "the Routers table wants a bare '7.23.3', and dropping the
// channel from the stored value (rather than only hiding it in the UI) also
// means switching stable→testing at the same release does not churn a write and
// a broadcast." Recomputed from the payload here, which is what the live update
// path does too (`(this.lastPayload.version || ”).replace(...)`).
func (s *System) reportIdentity(payload *SystemPayload) {
	if s.onIdentity == nil || payload == nil {
		return
	}
	serial := ""
	if payload.Serial != nil {
		serial = *payload.Serial
	}
	base := strings.TrimSpace(parenSuffix.ReplaceAllString(payload.Version, ""))
	key := payload.BoardName + " " + serial + " " + base
	if key == s.lastIdentityKey {
		return
	}
	s.lastIdentityKey = key
	s.onIdentity(Identity{Model: payload.BoardName, Serial: serial, OSVersion: base})
}

// systemFingerprint is the original's template string, separator for separator.
// A null temperature interpolates as the word "null" in JavaScript, so it does
// here too — the point is only that the string differs when a value does, but
// matching it exactly keeps the two implementations comparable by eye.
func systemFingerprint(p *SystemPayload) string {
	temp := "null"
	if p.TempC != nil {
		temp = strconv.FormatFloat(*p.TempC, 'f', -1, 64)
	}
	return strings.Join([]string{
		strconv.Itoa(p.CPULoad), strconv.Itoa(p.MemPct), strconv.Itoa(p.HddPct), temp,
		p.UptimeRaw, strconv.FormatBool(p.UpdateAvailable), p.LatestVersion,
	}, ",")
}

// readStatic reads the serial and the licence level once per connection. Both
// fail soft: a CHR or a virtual machine has no routerboard menu at all, and
// that is not an error worth reporting on a gauge card.
func (s *System) readStatic() {
	var serial, license *string
	if rows, err := s.ros.Do(systemRouterboardCmd); err == nil && len(rows) > 0 {
		if v := rows[0]["serial-number"]; v != "" {
			serial = &v
		}
	}
	if rows, err := s.ros.Do(systemLicenseCmd); err == nil && len(rows) > 0 {
		// `level` on a routerboard, `nlevel` on some x86 builds. Neither is
		// guaranteed, so the first one present wins.
		v := rows[0]["level"]
		if v == "" {
			v = rows[0]["nlevel"]
		}
		if v != "" {
			license = &v
		}
	}
	s.mu.Lock()
	s.staticRead = true
	s.serial, s.license = serial, license
	s.mu.Unlock()
}

func (s *System) readHealth() {
	rows, err := s.ros.Do(systemHealthCmd)
	s.mu.Lock()
	s.healthAt = time.Now()
	if err == nil {
		s.health = rows
	}
	s.mu.Unlock()
}

// checkForUpdates asks the router to ask MikroTik.
//
// THE ONLY CALL IN THIS COLLECTOR THAT LEAVES THE ROUTER, which is why it is
// rate limited to twelve hours, bounded in its retries, and never runs on the
// tick path. An update server that never settles must not become a sixty second
// poll against upgrade.mikrotik.com.
//
// Node shares this schedule across every SystemCollector for a router, because
// it builds up to three of them per router — the active session, the overview
// session and the alert session. This port builds ONE session per router, so
// the schedule lives on the collector. A second session type would need the
// shared map back.
func (s *System) checkForUpdates() {
	s.mu.Lock()
	if s.updateRuns || (!s.updateAt.IsZero() && time.Since(s.updateAt) < s.updateWindow()) {
		s.mu.Unlock()
		return
	}
	s.updateRuns = true
	s.updateAt = time.Now()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.updateRuns = false
		s.mu.Unlock()
	}()

	// A denied check is NOT transient and is not retried: only a permission
	// change fixes it. /print still succeeds on read permission alone and hands
	// back whatever the router last cached, so swallowing this would show stale
	// data and look healthy doing it. The word "unavailable" is deliberate —
	// the page styles a status matching it as a warning.
	_, checkErr := s.ros.Do(systemUpdateCheckCmd)
	denied := checkErr != nil && menuDenied(checkErr)

	rows, err := s.ros.Do(systemUpdatePrintCmd)
	row := routeros.Reply{}
	if err == nil && len(rows) > 0 {
		row = rows[0]
	}
	if denied {
		row = cloneReply(row)
		row["status"] = "Update check unavailable — API user needs write permission"
	}

	s.applyUpdate(row)

	// Retry only while the router says it is still working, and only a bounded
	// number of times.
	s.mu.Lock()
	if updateTransient(row) && s.updateTries < systemUpdateMaxRetries && !denied {
		s.updateTries++
		s.updateAt = time.Now().Add(-s.updateWindow()).Add(systemUpdateRetry)
	} else {
		s.updateTries = 0
	}
	s.mu.Unlock()
}

func (s *System) updateWindow() time.Duration { return systemUpdateInterval }

// updateTransient is the router still working on the answer: no version yet,
// and either nothing said or a status that says it is still looking. Caching
// that would make every later session believe the question had been answered.
func updateTransient(row routeros.Reply) bool {
	return UpdateUnknown(row["latest-version"], row["status"])
}

// UpdateUnknown reports that an update row states NO VERDICT — no version, and
// either no status or one that says the router is still working it out.
//
// ── ONE RULE, TWO CALLERS, AND THE SECOND ONE COST 50 ALERT ROWS ──────────
//
// The collector asks it as "should I retry?". `internal/alertwire` asks it as
// "may I let this payload resolve an open update alert?" — and those are the
// same question: a row with no verdict is not evidence the router is up to date.
//
// The wire first asked it with its own narrower test, `latest == "" && status ==
// ""`. That is a STRICT SUBSET: a transient status ("finding out latest
// version...") has a status, slipped through, and `updateVerdict` read it as
// false — resolving the alert. Four rows appeared after the first fix for
// exactly that reason, which is how the subset was found.
//
// Exported so there is one rule rather than two that must agree, which is the
// mistake `stripWanIP` and `res:move` both record.
func UpdateUnknown(latest, status string) bool {
	if latest != "" {
		return false
	}
	st := strings.ToLower(status)
	return st == "" || strings.Contains(st, "finding out") ||
		strings.Contains(st, "checking") || strings.Contains(st, "in progress")
}

// applyUpdate folds an update row into the cached payload and emits.
//
// It emits OUT OF BAND, without waiting for the next tick, and it does so even
// when the tick has not produced a payload yet — the startup check runs before
// the first gauge read, and the original discarded its result for exactly that
// reason until it was fixed.
func (s *System) applyUpdate(row routeros.Reply) {
	s.mu.Lock()
	s.update = row
	if s.last == nil {
		s.mu.Unlock()
		return
	}
	updated := *s.last
	updated.TS = time.Now().UnixMilli()
	updated.LatestVersion = row["latest-version"]
	updated.UpdateStatus = row["status"]
	updated.UpdateChannel = row["channel"]
	updated.UpdateAvailable = updateVerdict(row["latest-version"], row["status"],
		strings.TrimSpace(parenSuffix.ReplaceAllString(updated.Version, "")))
	s.last = &updated
	s.lastFp = ""
	s.mu.Unlock()

	s.emit("", "system:update", &updated)
}

func cloneReply(r routeros.Reply) routeros.Reply {
	out := routeros.Reply{}
	for k, v := range r {
		out[k] = v
	}
	return out
}

// PollMs is the collector's current poll period. Exported for callers that
// re-tune it and then need to confirm what took effect.
func (s *System) PollMs() int { return s.pollMs.ms() }
