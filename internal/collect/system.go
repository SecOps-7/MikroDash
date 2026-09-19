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
// ── THE NODE ORIGINAL STREAMS /system/resource, AND SO DOES THIS NOW ────────
//
// This said: "This side polls it instead — same rows, one fewer channel held
// open, which is what CLAUDE.md means by more efficient." That was a real
// decision on a real principle, and the principle stands; what changed is that
// the quantity it traded against was never measured.
//
//	the poll costs    ~30 commands a minute per router at the 2s cadence, and on
//	                  this fleet /system/resource/print was 111 a minute across
//	                  four routers -- the single largest item left
//	the stream costs  ONE channel, and B.0b searched to 24 concurrent channels on
//	                  live hardware (hAP AX3, RouterOS 7.24, two runs) and found
//	                  no ceiling, no starvation of the channels already open, and
//	                  no CPU trend
//
// So "one fewer channel" was avoiding a cost nobody has been able to observe, at
// the price of the largest command load in the app. The trade inverts, and the
// payload is identical either way -- which the original comment already said.
//
// ONE THING THE STREAM NEEDS THAT THE POLL DID NOT: a key. This menu is a
// SETTINGS menu, one row with no `.id`, so the rolling map is keyed by
// `keySingleton`. Keyed by the `.id` default it would hold nothing at all and the
// dashboard gauges would simply stop. See the note there.

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

// UpdateVerdict is the reading of an update row that BOTH the dashboard's
// system card and the Packages page make — literally the same function, because
// the same router state must not produce two different answers on two pages.
// `parseUpdate` in packages.go called it by copying it until 2026-09-16, which
// is how the two drifted apart for as long as they did.
//
// `latest-version` decides when the router has one. Otherwise the STATUS TEXT
// does, and the string matched is RouterOS's own: MikroTik's upgrade
// documentation scripts against `[/system/package/update get status] = "New
// version is available"`.
//
// ── AN OLDER `latest-version` IS NOT AN UPDATE ────────────────────────────
//
// The live app asked `latest !== installed`, and `testdata/system-update-cases.json`
// recorded that as "it is inequality, not ordering". MEASURED on the hAP ax³ on
// 2026-09-16: RouterOS 7.24.3 reported `latest-version: 7.24.2`, and both pages
// offered an Update button that would have DOWNGRADED the router. A stable
// channel that has pulled a build, or a router moved from testing to stable,
// reaches this state on its own.
//
// So the versions are ORDERED when both are plain dotted numbers, and the
// recorded case was changed deliberately. Anything this does not recognise —
// a development build like `7.25rc3` — still falls back to inequality rather
// than being ordered by a guess.
func UpdateVerdict(latest, status, installed string) bool {
	if latest != "" {
		if cmp, ok := rosVersionCmp(latest, installed); ok {
			return cmp > 0
		}
		return latest != installed
	}
	return strings.Contains(strings.ToLower(status), "new version")
}

// rosVersionCmp orders two RouterOS versions, reporting ok only when BOTH are
// plain dotted numbers — digits and dots, at least one component, nothing else.
//
// A missing component is zero, so `7.24` and `7.24.0` are the same version.
// `rc` and `beta` builds deliberately do not parse: ordering `7.25rc3` against
// `7.25beta5` has a real answer, and it is not one worth inventing here for a
// channel this app does not otherwise model.
func rosVersionCmp(a, b string) (int, bool) {
	av, aok := rosVersionParts(a)
	bv, bok := rosVersionParts(b)
	if !aok || !bok {
		return 0, false
	}
	for i := 0; i < len(av) || i < len(bv); i++ {
		x, y := 0, 0
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func rosVersionParts(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
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

	// board-name OR platform, for a device that reports no board at all.
	//
	// NOT FOR A CHR, WHICH THIS USED TO SAY. Measured against a real Cloud
	// Hosted Router (7.24.2, QEMU, 2026-09-06): a CHR reports
	// `board-name: CHR QEMU Standard PC (Q35 + ICH9, 2009)` and
	// `platform: MikroTik`, so the board name is present and it is the more
	// specific of the two — the fallback never fires for one. Whatever device
	// does report an empty board name, a card headed by neither field reads as
	// broken, so the fallback stays; only the claim about which device needs it
	// has been withdrawn.
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
		UpdateAvailable: UpdateVerdict(latest, status, installedBase),
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
	tableCore[SystemPayload]
	emit Emit

	// mu guards everything the update goroutine touches. It exists because the
	// update check can block for fifteen seconds and must not hold up the gauges.
	mu      sync.Mutex
	health  []routeros.Reply
	update  routeros.Reply
	serial  *string
	license *string

	// onIdentity is called when this router's hardware/firmware identity CHANGES.
	// See SetOnIdentity.
	onIdentity      IdentityFunc
	lastIdentityKey string

	staticRead  bool      // the serial and licence have been read for this connection
	firstTick   bool      // one reading has run, so the static read may happen now
	healthAt    time.Time // when health was last read
	updateAt    time.Time // when the update check last ran
	updateRuns  bool      // a check is in flight
	updateTries int
}

// NewSystem builds the collector. The bounds are Node's — the gauges are what
// the dashboard animates, so this is the fastest poll in the app.
func NewSystem(ros Reader, emit Emit, pollMs int) *System {
	s := &System{emit: emit, update: routeros.Reply{}}
	// Subscribed to /system/resource, the live gauge row. The static read and the
	// health window keep their own cadences, in `preRead`.
	s.setup(s, ros, pollMs, tableSpec{
		cmd: systemResourceCmd, poll: [3]int{2000, 500, 60000},
		// A SETTINGS MENU, NOT A TABLE. `/system/resource/print` returns one row
		// and it carries no `.id`, so the default key would drop it into
		// the fill's unkeyed counter and leave the entry empty -- which
		// for this collector means the dashboard's gauges stop. See keySingleton.
		streamKey: keySingleton,
	})
	return s
}

// DeferHealth pushes the health menu's next read a full interval out, so the
// NEXT Tick asks only for the gauges.
//
// ── WHY A ONE-SHOT READER NEEDS THIS ───────────────────────────────────────
//
// `healthAt` is the zero time on a fresh collector, so `time.Since` of it clears
// `systemHealthEvery` by a wide margin and the very FIRST Tick issues
// `/system/health/print` before `/system/resource/print`. For the polling
// collector that is right — health is due, and it is one read in thirty
// seconds. For a collector built to tick exactly once it doubles the cost, and
// the health row's only consumer is `TempC`, which no card on the Devices page
// renders.
//
// A caller that ticks repeatedly must not use this: it does not disable the
// health read, it postpones it, and the interval resumes from here.
func (s *System) DeferHealth() {
	s.mu.Lock()
	s.healthAt = time.Now()
	s.mu.Unlock()
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

// Start begins the gauge poll and kicks the one update check that runs at
// startup. Everything slower than the tick is scheduled from inside Tick, so
// there is one timer here rather than three.
func (s *System) Start() {
	s.tableCore.Start()
	go s.checkForUpdates()
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
func (s *System) reset() {
	s.mu.Lock()
	s.staticRead, s.firstTick = false, false
	s.serial, s.license = nil, nil
	s.healthAt = time.Time{}
	s.mu.Unlock()
}

// applyResource is what the scheduler calls with the resource row, and is
// everything Tick does once it has it. The static read and the health window
// stay on their own cadences above -- see scheduled.go on why a collector
// subscribes to ONE menu and reads the rest itself.
// preRead runs the two cadences that are not the resource row: the once-per-
// connection static read, and the health window.
func (s *System) preRead() {
	s.mu.Lock()
	doStatic := s.firstTick && !s.staticRead
	doHealth := time.Since(s.healthAt) >= systemHealthEvery
	// ── AND THE UPDATE CHECK, WHOSE RETRY WAS DEAD UNTIL 2026-09-10 ────────
	//
	// `checkForUpdates` schedules its own retry: an answer of "finding out
	// latest version..." is not a verdict, so it rewinds `updateAt` to come back
	// in a minute, up to three times. NOTHING EVER CALLED IT AGAIN — `Start` was
	// its only caller — so the retry was a permission granted to a function
	// nobody invoked, and a router whose check was still in flight when the
	// first print ran displayed "finding out latest version…" for the life of
	// the session.
	//
	// Reported by the operator on a router the check was mid-flight on; the
	// router itself said "New version is available" throughout.
	//
	// HERE BECAUSE THIS RUNS ON BOTH DELIVERY PATHS. `Tick` and `apply` both
	// call `preRead`, so wiring it to `Tick` alone would have fixed the polled
	// routers and left the streamed ones exactly as they were — which is the
	// shape of the split that was reported.
	//
	// The due-ness test is only an optimisation: `checkForUpdates` re-checks it
	// under the lock, so a race here cannot produce two checks. Without it this
	// would spawn a goroutine every tick to do nothing.
	doUpdate := !s.updateRuns &&
		(s.updateAt.IsZero() || time.Since(s.updateAt) >= s.updateWindow())
	s.mu.Unlock()

	if doUpdate {
		go s.checkForUpdates()
	}

	if doStatic {
		s.readStatic()
	}
	if doHealth {
		s.readHealth()
	}
}

// derive is the gauge row, after whatever `preRead` has due.
func (s *System) derive(rows []routeros.Reply, err error, _ bool) (*SystemPayload, string) {
	s.preRead()
	return s.applyResource(rows, err)
}

func (s *System) applyResource(rows []routeros.Reply, err error) (*SystemPayload, string) {
	if err != nil || len(rows) == 0 {
		return nil, ""
	}

	s.mu.Lock()
	s.firstTick = true
	payload := buildSystem(rows[0], s.health, s.update, s.serial, s.license, s.pollMs.ms())
	s.mu.Unlock()
	// The fingerprint is what the ORIGINAL compares, field for field: a gauge
	// that has not moved is not worth a frame. Note what is absent from it —
	// memory and disk totals never change, and the serial cannot.
	fp := systemFingerprint(payload)
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
	return payload, fp
}

func (s *System) send(p SystemPayload) {
	// ROUTER-WIDE, not a page room: these are the top bar's gauges, the
	// uptime chip and the RouterOS version row. A viewer sees them on every
	// page, so gating them on a page focus would blank the chrome.
	EvSystemUpdate.Emit(s.emit, "", p)
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
	// THROUGH THE CACHE: `packages` reads this menu too. This side reads it ONCE
	// PER CONNECTION, so it is the one that gains -- a serial number that has not
	// changed since boot costs nothing when packages has just fetched it.
	if rows, err := readVia(s.cache, s.ros, systemRouterboardCmd, s.pollMs.duration()); err == nil && len(rows) > 0 {
		if v := rows[0]["serial-number"]; v != "" {
			serial = &v
		}
	}
	if rows, err := s.ros.Do(systemLicenseCmd); err == nil && len(rows) > 0 {
		// `nlevel` ON A ROUTERBOARD, `level` ON A CHR — and this comment had it
		// the other way round until both were measured on 2026-09-06:
		//
		//	hAP (RouterOS 7)  software-id: HR2S-3YN6   nlevel: 6
		//	CHR 7.24.2        system-id: <redacted>     level: free
		//
		// The order below is unchanged and was always right; only the
		// explanation was wrong, which is the half a reader would have acted on.
		// Neither key is guaranteed, so the first one present wins.
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

	// THROUGH THE CACHE: `packages` reads this menu too.
	rows, err := readVia(s.cache, s.ros, systemUpdatePrintCmd, s.pollMs.duration())
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
// version...") has a status, slipped through, and `UpdateVerdict` read it as
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
	s.mu.Unlock()
	s.lastMu.Lock()
	if s.last == nil {
		s.lastMu.Unlock()
		return
	}
	updated := *s.last
	updated.TS = time.Now().UnixMilli()
	updated.LatestVersion = row["latest-version"]
	updated.UpdateStatus = row["status"]
	updated.UpdateChannel = row["channel"]
	updated.UpdateAvailable = UpdateVerdict(row["latest-version"], row["status"],
		strings.TrimSpace(parenSuffix.ReplaceAllString(updated.Version, "")))
	s.last = &updated
	s.lastFP = ""
	s.lastMu.Unlock()
	s.send(updated)
}

func cloneReply(r routeros.Reply) routeros.Reply {
	out := routeros.Reply{}
	for k, v := range r {
		out[k] = v
	}
	return out
}
