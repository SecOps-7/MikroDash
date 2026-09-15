package collect

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// tableTestReader counts reads and answers with whatever it is holding.
type tableTestReader struct {
	reads atomic.Int32
	mu    sync.Mutex
	rows  []routeros.Reply
	err   error
}

func (r *tableTestReader) Do(routeros.Cmd) ([]routeros.Reply, error) {
	r.reads.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows, r.err
}

func (r *tableTestReader) Connected() bool { return true }

func (r *tableTestReader) answer(rows []routeros.Reply, err error) {
	r.mu.Lock()
	r.rows, r.err = rows, err
	r.mu.Unlock()
}

type stubPayload struct{ Rows int }

// stubTable is the smallest table collector: its payload is the row count.
type stubTable struct {
	tableCore[stubPayload]
	noFingerprint bool

	mu     sync.Mutex
	sent   int
	resets int
}

func (s *stubTable) derive(rows []routeros.Reply, err error, _ bool) (*stubPayload, string) {
	if err != nil {
		if menuGone(err) {
			s.retire()
		}
		return nil, ""
	}
	if s.noFingerprint {
		return &stubPayload{Rows: len(rows)}, ""
	}
	return &stubPayload{Rows: len(rows)}, strconv.Itoa(len(rows))
}

func (s *stubTable) send(stubPayload) {
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()
}

func (s *stubTable) reset() { s.resets++ }

func (s *stubTable) sends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func newStubTable(r Reader, heartbeat time.Duration) *stubTable {
	s := &stubTable{}
	s.setup(s, r, 0, tableSpec{
		cmd: routeros.Cmd{Path: "/stub/print"}, poll: [3]int{60000, 1000, 60000}, heartbeat: heartbeat,
	})
	return s
}

var oneRow = []routeros.Reply{{".id": "*1"}}
var twoRows = []routeros.Reply{{".id": "*1"}, {".id": "*2"}}

// TestATableCollectorSendsOnChangeAndOnHeartbeat — the one emit gate.
func TestATableCollectorSendsOnChangeAndOnHeartbeat(t *testing.T) {
	s := newStubTable(&tableTestReader{}, 10*time.Second)
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return clock }

	s.apply(oneRow, nil)
	if s.sends() != 1 {
		t.Fatalf("the first reading was not sent: %d sends", s.sends())
	}
	clock = clock.Add(5 * time.Second)
	s.apply(oneRow, nil)
	if s.sends() != 1 {
		t.Errorf("an unchanged reading inside the heartbeat was sent: %d sends, want 1", s.sends())
	}
	s.apply(twoRows, nil)
	if s.sends() != 2 {
		t.Errorf("a changed reading was suppressed: %d sends, want 2", s.sends())
	}
	clock = clock.Add(11 * time.Second)
	s.apply(twoRows, nil)
	if s.sends() != 3 {
		t.Errorf("an unchanged reading past the heartbeat was suppressed: %d sends, want 3. "+
			"A card with a stale threshold goes stale on a quiet router.", s.sends())
	}
}

// TestAPayloadWithNoFingerprintIsSentEveryReading — `dhcpLeases` and `routing`.
func TestAPayloadWithNoFingerprintIsSentEveryReading(t *testing.T) {
	s := newStubTable(&tableTestReader{}, 0)
	s.noFingerprint = true
	s.apply(oneRow, nil)
	s.apply(oneRow, nil)
	if s.sends() != 2 {
		t.Errorf("%d sends for two readings with no fingerprint, want 2", s.sends())
	}
}

// TestAFailedReadKeepsTheLastPayload — one failed read must not blank a page.
func TestAFailedReadKeepsTheLastPayload(t *testing.T) {
	s := newStubTable(&tableTestReader{}, 0)
	s.apply(twoRows, nil)
	s.apply(nil, errors.New("timeout"))
	if p := s.Last(); p == nil || p.Rows != 2 {
		t.Errorf("after a failed read Last() = %+v, want the previous payload of 2 rows", p)
	}
	if s.sends() != 1 {
		t.Errorf("a failed read sent something: %d sends, want 1", s.sends())
	}
}

// TestARetiredCollectorStopsReadingUntilItReconnects — a menu the router does not
// have is not asked for again on the same connection, and is on the next one.
func TestARetiredCollectorStopsReadingUntilItReconnects(t *testing.T) {
	r := &tableTestReader{err: errors.New("no such command prefix")}
	s := newStubTable(r, 0)

	s.Tick()
	s.Tick()
	if n := r.reads.Load(); n != 1 {
		t.Fatalf("%d reads of a menu the router said it does not have, want 1", n)
	}
	s.apply(oneRow, nil)
	if s.sends() != 0 {
		t.Errorf("a retired collector still derived a delivered reading: %d sends", s.sends())
	}

	r.answer(oneRow, nil)
	s.Reconnected()
	t.Cleanup(s.Stop)
	if n := r.reads.Load(); n != 2 {
		t.Errorf("%d reads after a reconnect, want 2: the retirement outlived the connection", n)
	}
	if s.sends() != 1 || s.resets == 0 {
		t.Errorf("after a reconnect: %d sends and %d resets, want 1 send and a reset", s.sends(), s.resets)
	}
}

// TestAReconnectSendsAnIdenticalReading — the browser that reconnected has
// nothing on screen to compare against.
func TestAReconnectSendsAnIdenticalReading(t *testing.T) {
	r := &tableTestReader{rows: oneRow}
	s := newStubTable(r, 0)
	s.apply(oneRow, nil)
	s.Reconnected()
	t.Cleanup(s.Stop)
	if s.sends() != 2 {
		t.Errorf("%d sends, want 2: the first reading after a reconnect was suppressed as unchanged", s.sends())
	}
}

// TestTheFirstReadOnThePolledPathIsNotRepeatedByTheLoop — Start reads at once,
// and the loop it starts must wait out the interval rather than read again.
func TestTheFirstReadOnThePolledPathIsNotRepeatedByTheLoop(t *testing.T) {
	r := &tableTestReader{rows: oneRow}
	s := newStubTable(r, 0)
	s.Start()
	t.Cleanup(s.Stop)
	time.Sleep(150 * time.Millisecond)
	if n := r.reads.Load(); n != 1 {
		t.Errorf("%d reads within 150ms of Start on a 60s interval, want 1", n)
	}
}

func tableProbe[P any](c *tableCore[P]) (func(int), func() time.Duration, func() int, [3]int) {
	return c.SetPollMs, c.sched.cadence, c.PollMs, c.bounds
}

// TestEveryTableCollectorsCadenceFollowsARetune.
//
// `ppp`, `rosusers`, `dhcpNetworks` and `capsman` captured the interval they were
// constructed with in the scheduler's cadence, so a re-tune changed the `pollMs`
// the page reported and not how often the router was read. The list fails in
// both directions against the files that embed the core.
func TestEveryTableCollectorsCadenceFollowsARetune(t *testing.T) {
	type probe func() (func(int), func() time.Duration, func() int, [3]int)
	cases := map[string]probe{
		"arp.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewARP(nil, 0).tableCore)
		},
		"bridges.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewBridges(nil, Emit{}, nil, 0).tableCore)
		},
		"capsman.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewCapsman(nil, Emit{}, 0).tableCore)
		},
		"wan.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewWan(nil, Emit{}, nil, 0).tableCore)
		},
		"dhcpleases.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewDHCPLeases(nil, Emit{}, 0).tableCore)
		},
		"dhcpnetworks.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewDHCPNetworks(nil, Emit{}, nil, "", 0).tableCore)
		},
		"dns.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewDNS(nil, Emit{}, 0).tableCore)
		},
		"ipaddresses.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewIPAddresses(nil, Emit{}, 0).tableCore)
		},
		"netwatch.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewNetwatch(nil, Emit{}, 0).tableCore)
		},
		"packages.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewPackages(nil, Emit{}, 0).tableCore)
		},
		"ppp.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewPPP(nil, Emit{}, 0).tableCore)
		},
		"queues.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewQueues(nil, Emit{}, nil, 0).tableCore)
		},
		"rosusers.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewRosUsers(nil, Emit{}, nil, 0).tableCore)
		},
		"routing.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewRouting(nil, Emit{}, 0).tableCore)
		},
		"system.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewSystem(nil, Emit{}, 0).tableCore)
		},
		"talkers.go": func() (func(int), func() time.Duration, func() int, [3]int) {
			return tableProbe(&NewTalkers(nil, Emit{}, 0, 0).tableCore)
		},
	}

	for name, build := range cases {
		set, cadence, pollMs, b := build()
		x := b[1]
		if x == b[0] {
			x = b[2]
		}
		set(x)
		want := clampPoll(x, b[0], b[1], b[2])
		if got := pollMs(); got != want {
			t.Errorf("%s: pollMs %d after SetPollMs(%d), want %d", name, got, x, want)
		}
		if got := cadence(); got != time.Duration(want)*time.Millisecond {
			t.Errorf("%s: the scheduler's cadence is %s after a re-tune to %dms. The page reports "+
				"the new interval and the router is read at the old one.", name, got, want)
		}
		if b[1] != b[2] && want == b[0] {
			t.Errorf("%s: the re-tune landed on the default, so this case proves nothing", name)
		}
	}

	embeds := regexp.MustCompile(`(?m)^\ttableCore\[\w+\]$`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !embeds.Match(src) {
			if _, listed := cases[f]; listed {
				t.Errorf("%s is listed here and no longer embeds tableCore", f)
			}
			continue
		}
		found++
		if _, listed := cases[f]; !listed {
			t.Errorf("%s embeds tableCore and is not in this list, so its re-tune is unchecked", f)
		}
	}
	if found == 0 {
		t.Fatal("no file embeds tableCore, so the scan has stopped matching")
	}
}

// TestTableCollectorsKeepNoLifecycleOfTheirOwn.
//
// The core exists so there is one form of each lifecycle method. A table
// collector declaring its own again is the second form coming back, one method
// at a time. An override is allowed only where it is recorded here with its
// reason, and a recorded override that has gone fails too.
func TestTableCollectorsKeepNoLifecycleOfTheirOwn(t *testing.T) {
	overrides := map[string]string{
		"System.Start": "also kicks the one update check that runs at startup",
	}
	embeds := regexp.MustCompile(`(?m)^type (\w+) struct \{\n\ttableCore\[`)
	lifecycle := regexp.MustCompile(`(?m)^func \(\w+ \*(\w+)\) (Start|Stop|Suspend|Resume|Reconnected|UseCache|SetPollMs|PollMs|Last|RefreshNow|Tick|apply)\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	tables := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		types := map[string]bool{}
		for _, m := range embeds.FindAllStringSubmatch(src, -1) {
			types[m[1]] = true
			tables++
		}
		for _, m := range lifecycle.FindAllStringSubmatch(src, -1) {
			if !types[m[1]] {
				continue
			}
			key := m[1] + "." + m[2]
			if _, ok := overrides[key]; ok {
				seen[key] = true
				continue
			}
			t.Errorf("%s declares %s, which the table core already provides. Put the "+
				"difference in derive, send or reset, or record the override here with its reason.", f, key)
		}
	}
	for key := range overrides {
		if !seen[key] {
			t.Errorf("the recorded override %s no longer exists; remove the entry", key)
		}
	}
	if tables == 0 {
		t.Fatal("no type embeds tableCore as its first field, so this check sees nothing")
	}
}
