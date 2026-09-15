package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFastPollCollectorsSplitTheirSlowReads is the ledger for the operator's
// fast/slow rule.
//
// ── THE RULE ────────────────────────────────────────────────────────────────
//
// Set on 2026-09-08: a collector's LIVE reads keep its poll interval, and its
// reads of things that rarely change go to "a 30 second cadence or longer". The
// reason is the one CLAUDE.md states — the bottleneck is concurrent API channels
// on the MikroTik, so cost is commands x frequency, and a menu read 51 times a
// minute that changed once is 50 wasted commands.
//
// THE SAME SPLIT DECIDES DELIVERY (2026-09-15). Only live data may stream: a
// stream holds a channel open for the life of the subscription, which pays for a
// live read and never for a slow one. A slow lane and a metadata collector poll.
// `TestNoMetadataCollectorStreams` in internal/session keeps the stream table to
// that.
//
// It is not a new idea here. Seven collectors already hand-rolled it as a
// `ticks % <x>ConfigEvery` counter, and `system` as a `time.Since` window. What
// the rule did was find the two that had not: `ifStatus`, which was reading four
// menus at a ~1.2s poll and cost 76% of an idle router's entire load, and
// `routing`, which read two whole route tables every ten seconds.
//
// ── WHY A LEDGER AND NOT A COMMENT ──────────────────────────────────────────
//
// Because the rule is invisible in the thing it governs. A new collector that
// polls five menus at two seconds compiles, passes every test, renders correctly
// and is only ever found by measuring a router. Nothing else in this tree fails.
//
// IT FAILS IN BOTH DIRECTIONS, which is the discipline CLAUDE.md sets for every
// ledger here. A fast-polling collector missing from the table fails. A recorded
// exemption whose named constant no longer exists ALSO fails — so an exemption
// cannot outlive the situation it describes, and a `ConfigEvery` deleted in a
// refactor cannot leave a green comment behind claiming it is still there.
func TestFastPollCollectorsSplitTheirSlowReads(t *testing.T) {
	root := repoRoot(t)

	// ── the ledger ───────────────────────────────────────────────────────────
	//
	// Every collector whose default poll is under the threshold. `constant` names
	// the identifier that paces its slow lane; an empty one means the collector
	// has no slow half at all, and `why` has to say what makes that true.
	type lane struct {
		constant string
		why      string
	}
	ledger := map[string]lane{
		"system":    {"systemHealthEvery", "resource is live; health is windowed and the board/licence read is once per connection"},
		"talkers":   {"", "one menu, /ip/kid-control/device, and its per-device rates ARE the payload"},
		"conns":     {"", "one menu, /ip/firewall/connection, and a connection table is live by definition"},
		"bandwidth": {"", "reads the same connection table as conns; nothing in it is config"},
		"ifStatus":  {"ifStatusMetaTarget", "rates every poll; the three metadata menus every 30s"},
		"ping":      {"", "a =interval= stream, not a poll; there is no second read to pace"},
		"firewall":  {"", "the rule tables are read on Start and after a write; pollCounters reads only .id,packets,bytes"},
		"vlans":     {"vlanConfigEvery", ""},
		"ppp":       {"pppConfigEvery", ""},
		"bridges":   {"bridgeConfigEvery", ""},
		"queues":    {"", "both menus carry the live byte and rate counters the page exists to show"},
		"vpn":       {"", "peers, active-peers, installed-sa and ppp/active are all session state"},
		"routing":   {"routeConfigEvery", "BGP session state is what flaps; the route tables are the slow half"},
		"capsman":   {"capsConfigEvery", ""},
		"dns":       {"dnsConfigEvery", ""},
		// The one entry where BOTH halves have to be named, because the fast half
		// looks like it was missed. `/interface/detect-internet/state` and
		// `/ip/route` are read EVERY tick, outside the wanConfigEvery block, and
		// that is deliberate: they carry WAN failover state — which uplink is up,
		// which default route is active — and failover is the whole reason the
		// page exists. The interface list, the DHCP clients and the addresses are
		// the slow half. (The route read is unfiltered, so it is a large payload
		// on a big table; that is a payload cost, not a command one, and CLAUDE.md
		// is explicit that command count is the bottleneck.)
		"wan": {"wanConfigEvery", "detect-internet and /ip/route stay fast: they are failover state"},
	}

	// ── what the registry says is fast ───────────────────────────────────────
	var tables struct {
		Registry []struct {
			Key           string `json:"key"`
			DefaultPollMs int    `json:"defaultPollMs"`
			Pollable      bool   `json:"pollable"`
		} `json:"registry"`
	}
	raw := mustRead(t, filepath.Join(root, "internal", "collection", "collection_tables.json"))
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("collection_tables.json: %v", err)
	}

	// The threshold IS the rule's own number: at or above it a collector's poll
	// already satisfies "30 seconds or longer" and there is nothing to split.
	const thresholdMs = 30000

	src := collectSource(t, filepath.Join(root, "internal", "collect"))

	seen := map[string]bool{}
	for _, r := range tables.Registry {
		if !r.Pollable || r.DefaultPollMs <= 0 || r.DefaultPollMs >= thresholdMs {
			continue
		}
		seen[r.Key] = true
		l, ok := ledger[r.Key]
		if !ok {
			t.Errorf("collector %q polls every %dms and is not in the fast/slow ledger. "+
				"Either give it a slow lane for the reads that rarely change, or record "+
				"here why every one of its reads is live data.", r.Key, r.DefaultPollMs)
			continue
		}
		if l.constant == "" {
			if l.why == "" {
				t.Errorf("collector %q is recorded as having no slow lane with no reason given", r.Key)
			}
			continue
		}
		if !strings.Contains(src, l.constant) {
			t.Errorf("collector %q's ledger entry names %q, which is not in internal/collect any more. "+
				"The slow lane was removed or renamed and this entry outlived it.", r.Key, l.constant)
		}
	}

	for key := range ledger {
		if !seen[key] {
			t.Errorf("the ledger carries %q, which is no longer a pollable collector under %dms. "+
				"A recorded gap that has closed is a failure here.", key, thresholdMs)
		}
	}
}

// collectSource concatenates the non-test Go source of a directory, so a ledger
// entry can be checked against what is actually declared.
//
// NON-TEST ONLY, and that is the rule CLAUDE.md names: a scan that included this
// file would find every constant the ledger quotes, in the ledger itself, and
// prove nothing at all.
func collectSource(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var b strings.Builder
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || isTestSource(filepath.Join(dir, n)) {
			continue
		}
		b.WriteString(mustRead(t, filepath.Join(dir, n)))
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		t.Fatalf("no non-test source found under %s", dir)
	}
	return b.String()
}
