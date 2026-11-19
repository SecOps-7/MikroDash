package routers

import "testing"

// TestASwitchRowCarriesWhatTheSwitchKnowsAndNothingItDoesNot: model, version,
// uptime and ports from the poller; CPU, memory, update, licence and clients
// stay null, because a switch has no answer for them and a zero would read as
// one.
func TestASwitchRowCarriesWhatTheSwitchKnowsAndNothingItDoesNot(t *testing.T) {
	up := uint64(90061)
	hosts := 29
	rows := BuildStats(StatsSources{
		Routers: []StatsRouter{
			{ID: "r1", Label: "Edge"},
			{ID: "s1", Label: "Core switch", Kind: "swos"},
			{ID: "s2", Label: "Unread switch", Kind: "swos"},
			{ID: "s3", Label: "Hidden switch", Kind: "swos"},
		},
		Online:  map[string]bool{"s1": true},
		Visible: map[string]bool{"r1": true, "s1": true, "s2": true},
		Switches: map[string]SwitchStats{"s1": {Known: true, Connected: true, Model: "CSS610-8P-2S+",
			Version: "2.21", UptimeSec: &up, PortsUp: 7, PortsTotal: 10, Hosts: &hosts}},
	})
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3: a switch the viewer may not read is filtered like a router", len(rows))
	}
	r, s, unread := rows[0], rows[1], rows[2]
	if r.Kind != "routeros" || s.Kind != "swos" || unread.Kind != "swos" {
		t.Errorf("kinds %q %q %q", r.Kind, s.Kind, unread.Kind)
	}
	if !s.Online || !s.Known || s.BoardName == nil || *s.BoardName != "CSS610-8P-2S+" ||
		s.Version == nil || *s.Version != "2.21" || s.Uptime == nil || *s.Uptime != "1d1h1m1s" {
		t.Errorf("switch row %+v", s)
	}
	if s.PortsUp == nil || *s.PortsUp != 7 || *s.PortsTotal != 10 || s.Hosts == nil || *s.Hosts != 29 {
		t.Errorf("ports %v/%v hosts %v", s.PortsUp, s.PortsTotal, s.Hosts)
	}
	if s.CPU != nil || s.MemPct != nil || s.UpdateAvailable != nil || s.LicenseLevel != nil || s.Clients != nil {
		t.Error("a switch row claims a router-only reading")
	}
	// Not read yet: not known, so not "Offline" either.
	if unread.Known || unread.Online || unread.BoardName != nil {
		t.Errorf("unread switch %+v", unread)
	}
}

func TestUptimeIsWrittenTheRouterOSWay(t *testing.T) {
	for sec, want := range map[uint64]string{0: "0s", 59: "59s", 61: "1m1s", 3600: "1h0m0s", 694861: "1w1d1h1m1s"} {
		if got := FormatUptime(sec); got != want {
			t.Errorf("%d: %q, want %q", sec, got, want)
		}
	}
}
