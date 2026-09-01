package collect

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// funcBody returns one method's body, and fails when the anchor has moved rather
// than quietly measuring nothing.
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("%q not found in topology.go — this check is measuring nothing", decl)
	}
	rest := src[i+len(decl):]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("no end found for %q", decl)
	}
	return rest[:j]
}

// ── THE PING LOOP MUST NOT DRIVE THE STRUCTURE SWEEP ───────────────────────
//
// `topoPingStep` is 3s. `Tick` is five router commands: /ip/neighbor, the bridge
// host table and the wifi registration tables. `pingNext` used to call `Tick` on
// all three of its exit paths, so a collector whose configured interval is 30s
// re-read the router every 3s while the page was open -- the operator set one
// number and got another, an order of magnitude apart, with the expensive reads
// being the repeated ones.
//
// The split is by COST, not by data: structure comes from the router on the poll
// interval; everything derived in memory is republished as often as the ping
// loop turns, so latency and up/down state stay prompt.
func TestThePingLoopRepublishesRatherThanRereadingTheRouter(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("reading topology.go: %v", err)
	}
	src := string(b)

	body := funcBody(t, src, "func (t *Topology) pingNext() {")
	if regexp.MustCompile(`\bt\.Tick\(\)`).MatchString(body) {
		t.Error("pingNext calls Tick(): the 3s ping loop is driving the 5-command " +
			"structure sweep again, so the configured poll interval is ignored")
	}
	if !strings.Contains(body, "t.republish()") {
		t.Fatal("pingNext no longer republishes — ping results and up/down state " +
			"would not reach the page until the next structure sweep")
	}

	// AND republish MUST NOT READ THE ROUTER. That is the whole point: if it
	// grew a `t.ros.Do` it would be Tick under another name, at 3s.
	rp := funcBody(t, src, "func (t *Topology) republish() {")
	if strings.Contains(rp, "t.ros.") {
		t.Error("republish touches t.ros: it must rebuild from the last read, not " +
			"issue commands, or the 3s cadence is back on the router")
	}
	for _, read := range []string{"readHosts(", "readWifi(", "readDiscovery(", "readVlans("} {
		if strings.Contains(rp, read) {
			t.Errorf("republish calls %s, which reads the router", read)
		}
	}

	// The structure sweep still has to happen somewhere.
	if !strings.Contains(funcBody(t, src, "func (t *Topology) Start() {"), "t.Tick()") {
		t.Error("Start no longer ticks, so the first graph would wait a full interval")
	}
}

// Link rates are NOT republished here, and that is deliberate rather than an
// omission: they already reach the page on `ifstatus:update`, which the browser
// receives router-wide, and ifStatus reads every interface in ONE bulk command
// (`ifstatus.go` joins the names with commas). Adding a rates path here would
// duplicate a payload the page already has and cost a command already paid for.
//
// This pins the wiring that makes that true: topology's RateSource is the
// ifStatus collector, so the two cannot silently drift onto different sources.
func TestTopologyTakesItsRatesFromTheInterfaceCollector(t *testing.T) {
	b, err := os.ReadFile("../session/session.go")
	if err != nil {
		t.Skipf("session.go unreadable: %v", err)
	}
	if !regexp.MustCompile(`NewTopology\([^)]*s\.ifStatus`).Match(b) {
		t.Error("topology is no longer constructed with s.ifStatus as its RateSource — " +
			"link rates would need their own reads, which is the cost this avoids")
	}
}
