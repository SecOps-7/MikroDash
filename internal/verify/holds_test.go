package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryHoldReasonIsTaken - the third instance of one defect class today, and
// the first one with a gate.
//
// ── DECLARED AND NEVER FILLED ──────────────────────────────────────────────
//
// A hold is a reason a session stays alive and runs a named set of collectors.
// It is declared in three places and TAKEN in a fourth:
//
//	session.Reasons          a field per reason
//	session.reasonsLocked    reads `s.holds["<reason>"]` into that field
//	session.Needs            consults the reason's feed list
//	internal/server          `Retain(id, "<reason>")` - the only thing that sets it
//
// The first three are inert without the fourth, and NOTHING FAILS when it is
// missing: `Reasons.Devices` was read, `devicesFeeds` was consulted, and no code
// path ever took the hold. It cost nothing while `ifStatus` ran from connect on
// every session, and started costing the moment 4.2b gated it on demand - the
// Devices page's WAN RX/TX column was empty for every router, which is how it was
// eventually found.
//
// That is the same shape as `topology.ARPIP` (declared, used at two sites, never
// set) and `pollArp` (persisted, validated, read by nothing), both found the same
// day. This is the one that gets a gate, because a hold is the most expensive
// version: it decides what a router is asked for.
func TestEveryHoldReasonIsTaken(t *testing.T) {
	root := repoRoot(t)

	// The reasons, from the one place that maps a field to a key.
	needs := mustRead(t, filepath.Join(root, "internal", "session", "needs.go"))
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`s\.holds\["(\w+)"\]`).FindAllStringSubmatch(needs, -1) {
		declared[m[1]] = true
	}
	if len(declared) < 3 {
		t.Fatalf("read %d hold reasons out of reasonsLocked; there are at least three, "+
			"so the scan has stopped matching and this gate checks nothing", len(declared))
	}

	// And who takes them. `Retain` is the only way a hold is set: `holdOne`'s
	// table, by name, and any literal `Retain(id, "<reason>")` elsewhere.
	//
	// THE TABLE IS READ AS THE TABLE. This scanned every `{"x",` literal in the
	// package and then kept only four hard-coded words (`isHoldish`) so as not to
	// fire on unrelated struct literals, which meant a new hold reason taken and
	// never read could not fail the reverse check below. (Fleet holds use a
	// generated per-request name through a variable and read no reason, on
	// purpose: they keep a connection up for one request.)
	dir := filepath.Join(root, "internal", "server")
	holdsSrc := stripGoComments(mustRead(t, filepath.Join(dir, "fleet_holds.go")))
	start := strings.Index(holdsSrc, "func (s *Server) holdOne(")
	if start < 0 {
		t.Fatal("fleet_holds.go has no holdOne; this gate no longer knows where holds are taken")
	}
	body := holdsSrc[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	taken := map[string]bool{}
	for _, m := range regexp.MustCompile(`\{"(\w+)",`).FindAllStringSubmatch(body, -1) {
		taken[m[1]] = true
	}
	if len(taken) < 3 {
		t.Fatalf("read %d hold reasons out of holdOne's table; there are at least three, so "+
			"the scan has stopped matching", len(taken))
	}
	for _, name := range goFilesIn(t, dir) {
		src := stripGoComments(mustRead(t, filepath.Join(dir, name)))
		for _, m := range regexp.MustCompile(`Retain\([^,]+,\s*"(\w+)"\)`).FindAllStringSubmatch(src, -1) {
			taken[m[1]] = true
		}
	}

	var orphan []string
	for reason := range declared {
		if !taken[reason] {
			orphan = append(orphan, reason)
		}
	}
	sort.Strings(orphan)
	if len(orphan) > 0 {
		t.Errorf("%v are hold reasons the session reads and nothing in internal/server "+
			"ever takes. The field is read, the feed list is consulted, and the whole "+
			"path is dead - which is indistinguishable from a hold that is simply never "+
			"wanted, until a collector it feeds gets gated and a page goes blank.", orphan)
	}

	// The other direction: a reason taken and never read is a hold that keeps a
	// session alive and runs nothing, which is a connection held for no purpose.
	var unread []string
	for reason := range taken {
		if !declared[reason] {
			unread = append(unread, reason)
		}
	}
	sort.Strings(unread)
	if len(unread) > 0 {
		t.Errorf("%v are taken as holds and `reasonsLocked` reads none of them, so each "+
			"keeps a session alive and runs nothing", unread)
	}
}

// TestEveryStatsRouterFieldIsPopulated.
//
// ── TWICE ON ONE STRUCT, AND THE SECOND TIME HID BEHIND A FALLBACK ────────
//
// `routers.StatsRouter` is the record half of the Devices page's input, built in
// one literal in `internal/server/devices.go`. A field left out of that literal
// is the zero value, and every consumer downstream treats the zero value as an
// answer:
//
//	Geo        never set -> every device arrived with a nil location and the map
//	           dropped ALL of them into the "No location" tray
//	DefaultIf  never set -> the WAN interface resolved to the global default, so
//	           the RX/TX column was empty for any router whose WAN is not called
//	           what the global says
//
// The second one hid for longer because the fallback usually matches: a fleet
// where every router uses the default interface name looks perfectly correct.
// Only the router with a real WAN name showed the gap.
//
// So the literal must mention every field. That is a weaker property than "the
// value is right" and a much stronger position than a comment, because it refuses
// the shape rather than catching an instance.
func TestEveryStatsRouterFieldIsPopulated(t *testing.T) {
	root := repoRoot(t)

	src := mustRead(t, filepath.Join(root, "internal", "routers", "assemble.go"))
	decl := regexp.MustCompile(`(?s)type StatsRouter struct \{(.*?)\n\}`).FindStringSubmatch(src)
	if decl == nil {
		t.Fatal("StatsRouter has moved; this gate is reading nothing")
	}
	var fields []string
	for _, line := range strings.Split(decl[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if m := regexp.MustCompile(`^([A-Z]\w*)\s`).FindStringSubmatch(line); m != nil {
			fields = append(fields, m[1])
		}
	}
	if len(fields) < 5 {
		t.Fatalf("read %d fields off StatsRouter; it has more, so the parse has "+
			"drifted and this gate checks nothing", len(fields))
	}

	built := mustRead(t, filepath.Join(root, "internal", "server", "devices.go"))
	lit := regexp.MustCompile(`(?s)routers\.StatsRouter\{(.*?)\n\t\t\}`).FindStringSubmatch(built)
	if lit == nil {
		t.Fatal("the StatsRouter literal has moved in devices.go")
	}
	body := stripGoComments(lit[1])

	var missing []string
	for _, f := range fields {
		if !regexp.MustCompile(`\b` + f + `:`).MatchString(body) {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%v are StatsRouter fields the Devices page's builder never sets, so "+
			"each reaches every consumer as its zero value and is read as an answer.",
			missing)
	}
}
