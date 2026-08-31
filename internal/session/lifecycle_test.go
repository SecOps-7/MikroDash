package session

// Every collector this session builds must be reachable from something that
// starts it.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// `netwatch` was constructed here, given an accessor nobody called, and never
// started. It polled only if the connection dropped and came back, so on a
// healthy router its Dashboard card stayed empty indefinitely. Nothing caught
// it: it compiles, it has tests, its collector emits the right event, and the
// event audit is satisfied because the emit STRING is in the source. "Present in
// the source" and "reachable at runtime" are different claims, and only the
// first one was being checked anywhere.
//
// A source check, and a deliberately narrow one: it reads the two files that
// own collector lifecycle and asks whether each constructed collector appears
// with `.Start()` or `.Resume()` somewhere — here, or in the server through its
// accessor. It does not prove the call runs, only that a path to it exists,
// which is exactly the thing netwatch lacked.

import (
	"mikrodash/internal/hub"
	"mikrodash/internal/store"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Not a collector: a shared lookup table that `conns` and `bandwidth` both hold
// by reference. It has no reader, no poll and nothing to start.
var notPolled = map[string]string{
	"connTable": "a shared lookup table passed to conns and bandwidth, not a poller",
}

// dormancyKeyFor maps a session FIELD name onto the registry KEY the funnel is
// called with. They are the same word for every collector but two, and those two
// are named here rather than guessed.
func dormancyKeyFor(field string) string {
	switch field {
	case "rosUsers":
		return "rosusers"
	case "conns":
		return "conns"
	}
	for _, k := range targetKeys {
		if k == field {
			return k
		}
	}
	return ""
}

func readServerSources(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "server")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(body)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestEveryCollectorHasAPathThatStartsIt(t *testing.T) {
	body, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	sess := string(body)
	srv := readServerSources(t)

	built := regexp.MustCompile(`s\.(\w+)\s*=\s*collect\.New\w+\(`)
	// WHITESPACE-TOLERANT, and that matters: these accessors are column-aligned,
	// so a pattern expecting one space before the brace found exactly one of the
	// twenty-three and reported twenty-two collectors as unstartable. The first
	// version of this check did that, and its answer looked alarming and was
	// almost entirely wrong.
	accRe := regexp.MustCompile(`func \(s \*Session\)\s+(\w+)\(\)\s*\*collect\.\w+\s*\{\s*return\s+s\.(\w+)\s*\}`)

	accessor := map[string]string{}
	for _, m := range accRe.FindAllStringSubmatch(sess, -1) {
		accessor[m[2]] = m[1]
	}
	if len(accessor) < 15 {
		t.Fatalf("only %d accessors matched — the pattern has drifted from the source, and a "+
			"check that cannot see the accessors reports every page collector as unstarted",
			len(accessor))
	}

	seen := map[string]bool{}
	var unstarted []string
	for _, m := range built.FindAllStringSubmatch(sess, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := notPolled[name]; ok {
			continue
		}
		if regexp.MustCompile(`s\.` + name + `\.(Start|Resume)\(\)`).MatchString(sess) {
			continue
		}
		if a := accessor[name]; a != "" &&
			regexp.MustCompile(`\.`+a+`\(\)\s*\.\s*(Start|Resume)\(\)`).MatchString(srv) {
			continue
		}
		// ── THE FUNNEL IS A START PATH TOO (added 2026-08-28) ───────────────
		//
		// `ws.go` used to call `cn.rsession.X().Resume()` directly at twenty
		// sites, each behind its own `CollectorEnabled` check. They now go
		// through `Session.ResumeCollector(key)`, which is the live
		// `_resumeCollector` — one place that checks enabled AND consults the
		// dormancy veto, so "a gate that knows nothing about dormancy cannot
		// undo it".
		//
		// This gate went red the moment those calls disappeared, which is it
		// working: the path really did change. Taught rather than relaxed — the
		// key must be BOTH passed to the funnel in internal/server and present in
		// `targetKeys`, because an unknown key is a silent no-op.
		// `TestEveryKeyWsPassesIsInTheTable` pins the second half.
		if k := dormancyKeyFor(name); k != "" &&
			regexp.MustCompile(`ResumeCollector\("`+k+`"\)`).MatchString(srv) {
			continue
		}
		unstarted = append(unstarted, name)
	}
	if len(seen) < 20 {
		t.Fatalf("only %d collectors found in session.go — the construction pattern has drifted",
			len(seen))
	}

	sort.Strings(unstarted)
	for _, name := range unstarted {
		t.Errorf("`%s` is constructed but nothing starts it: no s.%s.Start()/Resume() here, and "+
			"no accessor call in internal/server either. A collector reachable only from "+
			"Reconnected() polls nothing until the connection drops and returns.", name, name)
	}
}

// TestTheNotPolledListIsStillTrue — an entry that gained a Start must leave,
// or the list becomes a place things hide.
func TestTheNotPolledListIsStillTrue(t *testing.T) {
	body, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	sess := string(body)
	for name, why := range notPolled {
		if regexp.MustCompile(`s\.` + name + `\.(Start|Resume)\(\)`).MatchString(sess) {
			t.Errorf("`%s` is listed as not-polled (%q) but something starts it now — remove the entry",
				name, why)
		}
		if !strings.Contains(sess, "s."+name+" = collect.New") {
			t.Errorf("`%s` is listed as not-polled but is no longer constructed — remove the entry", name)
		}
	}
}

// TestTheBackgroundCollectorCountIsRecorded pins how many collectors a Session
// starts the moment it connects — before anybody opens a page.
//
// ── WHY A COUNT IS WORTH A TEST ─────────────────────────────────────────────
//
// It is the number the Routers-page decision rests on. The port record compares
// it against the live overview pool's THREE (`overviewSessions.js:102-114`:
// system, interfaceStatus, dhcpLeases) to say how much heavier a Go background
// pool would be on every router in a fleet — and the documented bottleneck is
// concurrent API channels on the MikroTik, not CPU.
//
// **That number was recorded as "~11" and was 14 when counted.** It drifted
// because every collector wired into `Session` lands in the connect path too, so
// it grows quietly with unrelated work and nothing said so. An operator reading
// "roughly four times the live pool" was reading 4.7×.
//
// So the count is asserted rather than described. Adding a collector to the
// connect path now fails this test, which is the prompt to update the two places
// that quote it — and to ask whether the new one belongs in a background session
// at all, which is the question the drift hid.
func TestTheBackgroundCollectorCountIsRecorded(t *testing.T) {
	// Counted 2026-08-25: bridges dhcpLeases dhcpNetworks dns firewall ifStatus
	// logs netwatch ping system talkers traffic vlans wan.
	const recorded = 14

	body, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	sess := string(body)

	// The connect block only — `Reconnected()` and the page-focus resumes are a
	// different question and must not be counted here.
	from := strings.Index(sess, "if first {")
	if from < 0 {
		t.Fatal("the connect block's `if first {` has moved — this count is measuring the wrong region")
	}
	end := strings.Index(sess[from:], "\n\t\t}")
	if end < 0 {
		t.Fatal("cannot find the end of the connect block")
	}
	block := sess[from : from+end]
	// Guard against the region silently swallowing the rest of the file, which
	// is how a lifted slice usually goes wrong here.
	if strings.Contains(block, "Reconnected()") {
		t.Fatal("the connect block ran past its end and took in the reconnect path")
	}

	starts := map[string]bool{}
	for _, m := range regexp.MustCompile(`s\.(\w+)\.Start\(\)`).FindAllStringSubmatch(block, -1) {
		starts[m[1]] = true
	}
	if len(starts) != recorded {
		names := make([]string, 0, len(starts))
		for n := range starts {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("a Session starts %d collectors on connect, recorded %d: %v\n"+
			"If that is deliberate, update this constant AND the two places that quote it "+
			"(the port record's Routers item and CLAUDE.md's cutover blockers) — the number is the "+
			"basis of the background-pool decision, and it went stale once already.",
			len(starts), recorded, names)
	}
}

// #105: EVERY CONNECT-TIME START IS GATED, and every constructor takes a
// RESOLVED interval rather than a hard-coded zero.
//
// Read from the source rather than driven, for the same reason
// TestTheBackgroundCollectorCountIsRecorded is: standing a Session up needs a
// router. What it pins is that the wiring cannot be half-undone — a Start that
// loses its guard, or a constructor that goes back to `0`, is the regression
// here, and both are invisible to a test that only counts collectors.
func TestEveryCollectorHonoursTheResolvedConfig(t *testing.T) {
	body, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	sess := string(body)

	from := strings.Index(sess, "if first {")
	if from < 0 {
		t.Fatal("the connect block's `if first {` has moved")
	}
	end := strings.Index(sess[from:], "\n\t\t}")
	if end < 0 {
		t.Fatal("cannot find the end of the connect block")
	}
	block := sess[from : from+end]

	// Every started collector must sit inside an Enabled guard naming ITS key.
	for _, m := range regexp.MustCompile(`s\.(\w+)\.Start\(\)`).FindAllStringSubmatch(block, -1) {
		name := m[1]
		guard := `s.eff.Enabled["` + name + `"]`
		if !strings.Contains(block, guard) {
			t.Errorf("%s.Start() is not gated on %s — a collector the operator turned off "+
				"for this router would still be started", name, guard)
		}
	}

	// THE RECONNECT BRANCH TOO. `Reconnected()` is not the latch-clearing no-op
	// its name suggests: every implementation ends `Tick(); loop.start()`, so an
	// ungated one RESTARTS a collector the operator turned off — and reconnects
	// are routine, the usual cause being a router upgrade.
	//
	// This was missed when the starts were gated, and the mutation that ungated
	// one survived until this block existed.
	relse := strings.Index(sess, "\t\t} else {")
	if relse < 0 {
		t.Fatal("the reconnect branch has moved")
	}
	rend := strings.Index(sess[relse:], "\n\t\t}")
	rblock := sess[relse : relse+rend]
	for _, m := range regexp.MustCompile(`s\.(\w+)\.Reconnected\(\)`).FindAllStringSubmatch(rblock, -1) {
		name := m[1]
		// `rosUsers` is the one field whose spelling differs from its registry
		// key, which is `rosusers`.
		key := name
		if key == "rosUsers" {
			key = "rosusers"
		}
		if !strings.Contains(rblock, `s.eff.Enabled["`+key+`"]`) {
			t.Errorf("%s.Reconnected() is not gated on the enabled set — a disabled collector "+
				"comes back after any reconnect", name)
		}
	}

	// And every constructor must take a RESOLVED interval.
	//
	// Asserting `s.eff.Poll[` is present is the right check; an earlier version
	// looked for a trailing `, 0)` and flagged `NewTalkers(..., Poll["talkers"],
	// 0)`, whose final zero is `topN`. Matching on what must BE there beats
	// matching on what must not.
	//
	// EXEMPT, each because it has no poll interval to resolve:
	exempt := map[string]string{
		"collect.NewLogs":      "streams /log/listen; no interval in its signature",
		"collect.NewConnTable": "a shared table, not a collector",
		"collect.NewTraffic":   "streams monitor-traffic; its trailing 5 is a sample window, not a poll",
	}
	for _, m := range regexp.MustCompile(`s\.\w+ = (collect\.New\w+)\([^\n]*`).FindAllStringSubmatch(sess, -1) {
		if _, ok := exempt[m[1]]; ok {
			continue
		}
		if !strings.Contains(m[0], `s.eff.Poll[`) {
			t.Errorf("%s does not take a resolved interval: %s", m[1], strings.TrimSpace(m[0]))
		}
	}
}

// The record's `collection` block actually REACHES the resolution.
//
// The gating tests above read the source; this one drives `Acquire` against a
// real store, because "every Start is guarded" and "the guard is fed the
// router's own config" are different claims and only the second one catches a
// resolution built from `nil`.
//
// The router is unreachable on purpose — nothing here needs it to connect, and
// the connect loop retrying in the background is what `Release` stops.
func TestTheRecordsCollectionBlockReachesTheResolution(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", `{}`)
	// `wan` is disableable in the registry, so turning it off must reach
	// `Enabled`. The password is not encrypted, which `Routers()` records as a
	// problem and carries on from — the router is still returned.
	write("routers.json", `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
	  "username":"u","password":"","collection":{"off":["wan"]}}]`)

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, hub.New())
	defer m.Shutdown()

	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release("r1")

	if s.eff.Enabled["wan"] {
		t.Error("`wan` is in the router's off list and resolved as ENABLED — the record's " +
			"collection block is not reaching Resolve")
	}
	if !s.eff.Enabled["dns"] {
		t.Error("`dns` resolved as disabled; only `wan` was turned off")
	}
}
