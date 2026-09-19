package diag

import (
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// A RUN IN FLIGHT IS FOLDED ONLY UP TO THE SECTION STILL ARRIVING. The
// traceroute capture's second run holds sections 0, 1, 1, 2, 2, 2, 3, 3, 3: cut
// after the second row of section 2, the complete part is 0, 1, 1 — a two-hop
// table, not a three-hop one with its third hop missing.
func TestARunInFlightIsFoldedUpToTheSectionStillArriving(t *testing.T) {
	c := readCapture(t, "toolTraceroute.json", 2)
	rows := c.Exchanges[1].Rows
	if got := CompleteSections(rows[:5]); len(got) != 3 {
		t.Fatalf("complete part of 5 rows is %d rows, want 3", len(got))
	}
	if r := FoldTraceroute("198.51.100.1", CompleteSections(rows[:5])); len(r.Hops) != 2 {
		t.Errorf("the table in flight has %d hops, want 2", len(r.Hops))
	}
	// The control: the whole run is folded as it always was.
	if got := CompleteSections(rows); len(got) != 6 {
		t.Errorf("complete part of the finished run is %d rows, want the 6 before its last section", len(got))
	}
	// Ping's rows carry no section, and each is complete.
	p := readCapture(t, "toolPing.json", 2).Exchanges[0].Rows
	if got := CompleteSections(p); len(got) != len(p) {
		t.Errorf("ping: %d of %d rows kept", len(got), len(p))
	}
	if got := CompleteSections(nil); len(got) != 0 {
		t.Errorf("nothing yet: %v", got)
	}
	if got := CompleteSections([]routeros.Reply{{".section": "0"}}); len(got) != 0 {
		t.Errorf("one section still arriving is not complete: %v", got)
	}
}

// A CONTINUOUS RUN IS BOUNDED, AND ITS ANSWER IS UNCHANGED BY THE BOUND.
// KeepLast keeps the newest rows; ping's summary is the last row's, so a trimmed
// run still reports the whole run's totals.
func TestAContinuousPingKeepsItsLatestRowsAndItsTotals(t *testing.T) {
	var rows []routeros.Reply
	for i := 0; i < 250; i++ {
		rows = append(rows, routeros.Reply{"seq": strconv.Itoa(i), "host": "198.51.100.1", "time": "5ms",
			"sent": strconv.Itoa(i + 1), "received": strconv.Itoa(i + 1), "packet-loss": "0"})
	}
	kept := KeepLast(PingKeepReplies)(rows)
	if len(kept) != PingKeepReplies || kept[0]["seq"] != "150" {
		t.Fatalf("kept %d rows starting at seq %s, want the latest %d", len(kept), kept[0]["seq"], PingKeepReplies)
	}
	r := FoldPing("198.51.100.1", kept)
	if r.Sent != 250 || len(r.Replies) != PingKeepReplies {
		t.Errorf("a trimmed run says sent %d with %d replies, want 250 and %d", r.Sent, len(r.Replies), PingKeepReplies)
	}
	if len(KeepLast(5)(rows[:3])) != 3 {
		t.Error("KeepLast cut a run shorter than its bound")
	}
}

// A CONTINUOUS TORCH AVERAGES ITS LAST TorchWindow SECONDS. Ten seconds of one
// flow at 1000, 2000 … 10000 bit/s: KeepLastSections keeps the last five whole
// seconds and the one arriving, CompleteSections cuts the one arriving, and the
// fold averages 5000..9000 to 7000. Averaged over the run it would be 5500.
func TestAContinuousTorchAveragesItsLastSeconds(t *testing.T) {
	var rows []routeros.Reply
	for sec := 0; sec < 10; sec++ {
		for _, port := range []string{"443", "80"} {
			rows = append(rows, routeros.Reply{".section": strconv.Itoa(sec), "ip-protocol": "tcp",
				"src-address": "198.51.100.1", "src-port": port, "dst-address": "198.51.100.2", "dst-port": "5000",
				"rx": strconv.Itoa((sec + 1) * 1000), "tx": "0"})
		}
	}
	kept := KeepLastSections(TorchWindow)(rows)
	if len(kept) != (TorchWindow+1)*2 || kept[0][".section"] != "4" {
		t.Fatalf("kept %d rows from section %s, want %d from section 4", len(kept), kept[0][".section"], (TorchWindow+1)*2)
	}
	r := FoldTorch("ether1", CompleteSections(kept))
	if r.Reports != TorchWindow {
		t.Fatalf("the window has %d seconds, want %d", r.Reports, TorchWindow)
	}
	if got := r.Flows[0].RxBps; got != 7000 {
		t.Errorf("the rolling rate is %d bit/s, want 7000 (the last five whole seconds)", got)
	}
	if n := len(KeepLastSections(TorchWindow)(rows[:4])); n != 4 {
		t.Errorf("a run shorter than the window lost rows: kept %d of 4", n)
	}
}

// A CONTINUOUS COMMAND HAS NO COUNT OR DURATION, AND THE HOUR IS ITS BOUND.
func TestAContinuousRunIsBoundedByTheHour(t *testing.T) {
	p, err := PingCommand("198.51.100.1", 5, true)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := TorchCommand("ether1", 5, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []routeros.Cmd{p, tc} {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "=count=") || strings.HasPrefix(a, "=duration=") {
				t.Errorf("%s continuous carries %s, so it would end by itself", c.Path, a)
			}
		}
		if c.Timeout != ContinuousMax {
			t.Errorf("%s continuous times out at %v, want %v", c.Path, c.Timeout, ContinuousMax)
		}
	}
	if _, err := PingCommand("-bad", 5, true); err == nil {
		t.Error("a continuous ping skipped the address check")
	}
	if _, err := TorchCommand("=bad", 5, true); err == nil {
		t.Error("a continuous torch skipped the interface check")
	}
}
