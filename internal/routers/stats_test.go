package routers

// What this pins, and why each case exists.
//
// The row is rendered by a page that must not change, so the assertions are on
// MARSHALLED JSON wherever key presence or null-versus-zero is the point. A
// struct comparison would pass on a row that serialised `0` where the original
// sends `null`, and the difference between those two is a router reported as
// healthy-and-idle rather than as not-yet-heard-from.

import (
	"encoding/json"
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/geoplace"
)

func ptr[T any](v T) *T { return &v }

func fullSystem() *collect.SystemPayload {
	return &collect.SystemPayload{
		UptimeRaw: "3w4d5h", CPULoad: 7, MemPct: 42, HddPct: 13,
		Version: "7.24", BoardName: "hAP ax^3",
		Arch: ptr("arm64"), Serial: ptr("HGL09XY1ZQ2"), LicenseLevel: ptr("4"),
	}
}

func fields(t *testing.T, r Row) map[string]any {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestAnAbsentPayloadSendsNullNotZero is the one that matters most. A router
// whose system payload has not arrived renders "—"; one genuinely idle renders
// "0%". Flattening the two reports every unreachable router as a healthy one.
func TestAnAbsentPayloadSendsNullNotZero(t *testing.T) {
	m := fields(t, BuildRow(Input{ID: "r-A", Label: "Branch", Host: "10.0.0.2"}, nil, nil))

	for _, k := range []string{
		"cpu", "uptime", "memPct", "hddPct", "version", "boardName", "arch",
		"serial", "licenseLevel", "updateAvailable", "latestVersion", "clients", "geo",
		"siteId", "siteName", "lastError",
	} {
		v, present := m[k]
		if !present {
			t.Errorf("%q is missing from the payload entirely; the page reads it", k)
			continue
		}
		if v != nil {
			t.Errorf("%q = %#v with no payload, want null — a router that has not "+
				"been heard from must not render as one at rest", k, v)
		}
	}

	// AND THE COUNT IS STILL A NUMBER. `openAlerts[r.id] || 0` in the original,
	// so a router with nothing open reports 0 rather than null.
	if m["openAlerts"] != float64(0) {
		t.Errorf("openAlerts = %#v, want 0", m["openAlerts"])
	}
}

// TestAGenuineZeroIsReportedAsZero — the other half of the same rule. An idle
// router really does report 0% CPU, and that must survive.
func TestAGenuineZeroIsReportedAsZero(t *testing.T) {
	sys := fullSystem()
	sys.CPULoad, sys.MemPct, sys.HddPct = 0, 0, 0
	m := fields(t, BuildRow(Input{ID: "r-A", System: sys}, nil, nil))

	for _, k := range []string{"cpu", "memPct", "hddPct"} {
		if m[k] != float64(0) {
			t.Errorf("%q = %#v, want 0 — an idle router reports zero and it must "+
				"not be confused with silence", k, m[k])
		}
	}
	// A DHCP payload with no leases is likewise 0, not null.
	m = fields(t, BuildRow(Input{ID: "r-A", DHCPLeases: &collect.LeasesPayload{}}, nil, nil))
	if m["clients"] != float64(0) {
		t.Errorf("clients = %#v for an empty lease list, want 0", m["clients"])
	}
}

// TestThePayloadsOwnNullsAreCarriedThrough — Arch, Serial and LicenseLevel are
// already pointers, because "not read yet" and "this router has no routerboard"
// must both render as nothing. Flattening them to "" would render as nothing
// while claiming to be an answer.
func TestThePayloadsOwnNullsAreCarriedThrough(t *testing.T) {
	sys := fullSystem()
	sys.Arch, sys.Serial, sys.LicenseLevel = nil, nil, nil
	m := fields(t, BuildRow(Input{ID: "r-A", System: sys}, nil, nil))

	for _, k := range []string{"arch", "serial", "licenseLevel"} {
		if v, present := m[k]; !present || v != nil {
			t.Errorf("%q = %#v, want null — a router that reports no %s is not "+
				"the same as one this app has not asked", k, v, k)
		}
	}
	// The non-pointer fields beside them still come through.
	if m["version"] != "7.24" || m["boardName"] != "hAP ax^3" {
		t.Errorf("the rest of the payload was lost: %v / %v", m["version"], m["boardName"])
	}
}

// TestTheUpdateVerdictIsNullUntilChecked — the card's "Update" pill reads this,
// and the three states are different facts. Not yet checked is NULL; checked and
// current is FALSE; checked and behind is TRUE with the version. A port that sent
// false before the check had answered would tell an operator their router was
// current when nothing had asked.
//
// (Replaces `TestOnlyTheDefaultInterfacesRatesAreShown`, deleted 2026-10-01 with
// the card's WAN RX/TX.)
func TestTheUpdateVerdictIsNullUntilChecked(t *testing.T) {
	m := fields(t, BuildRow(Input{ID: "r", System: &collect.SystemPayload{Version: "7.24"}}, nil, nil))
	if m["updateAvailable"] != nil || m["latestVersion"] != nil {
		t.Errorf("unchecked: updateAvailable=%v latestVersion=%v, want both null", m["updateAvailable"], m["latestVersion"])
	}
	m = fields(t, BuildRow(Input{ID: "r", System: &collect.SystemPayload{Version: "7.24",
		LatestVersion: "7.24", UpdateStatus: "System is already up to date"}}, nil, nil))
	if m["updateAvailable"] != false {
		t.Errorf("checked and current: updateAvailable=%v, want false (not null)", m["updateAvailable"])
	}
	m = fields(t, BuildRow(Input{ID: "r", System: &collect.SystemPayload{Version: "7.24",
		LatestVersion: "7.25", UpdateStatus: "New version is available", UpdateAvailable: true}}, nil, nil))
	if m["updateAvailable"] != true || m["latestVersion"] != "7.25" {
		t.Errorf("behind: updateAvailable=%v latestVersion=%v, want true and 7.25", m["updateAvailable"], m["latestVersion"])
	}
}

// TestLastErrorOnlyWhenDisconnected — the original ignores it while connected,
// so a router that recovered must not keep explaining an outage that ended.
func TestLastErrorOnlyWhenDisconnected(t *testing.T) {
	m := fields(t, BuildRow(Input{
		ID: "r-A", Connected: true, LastError: "dial tcp: connection refused",
	}, nil, nil))
	if m["lastError"] != nil {
		t.Errorf("lastError = %#v while connected, want null", m["lastError"])
	}

	m = fields(t, BuildRow(Input{
		ID: "r-A", Connected: false, LastError: "dial tcp: connection refused",
	}, nil, nil))
	if m["lastError"] != "dial tcp: connection refused" {
		t.Errorf("lastError = %#v while offline, want the message", m["lastError"])
	}
}

// TestOpenAlertsAreIndependentOfReachability — a router can be reachable and
// still have something wrong on it.
func TestOpenAlertsAreIndependentOfReachability(t *testing.T) {
	counts := map[string]int{"r-A": 3}
	m := fields(t, BuildRow(Input{ID: "r-A", Connected: true}, counts, nil))
	if m["openAlerts"] != float64(3) {
		t.Errorf("openAlerts = %#v, want 3", m["openAlerts"])
	}
	// A router the grouped query never mentioned reads as 0 through the map,
	// which is what the original's `|| 0` does with an absent key.
	m = fields(t, BuildRow(Input{ID: "r-B", Connected: false}, counts, nil))
	if m["openAlerts"] != float64(0) {
		t.Errorf("openAlerts = %#v for an unmentioned router, want 0", m["openAlerts"])
	}
}

// ── the site and the location ────────────────────────────────────────────────

func siteSet() map[string]Site {
	return map[string]Site{"site-1": {
		Name: "HQ",
		Row: &geoplace.SiteRow{
			Name: "HQ", Lat: 48.85, Lon: 2.35,
			PlaceName: "Paris", PlaceRegion: "IDF", PlaceCC: "FR",
		},
	}}
}

func TestASiteNamesTheRouterAndPlacesItLast(t *testing.T) {
	m := fields(t, BuildRow(Input{ID: "r-A", SiteIDs: []string{"site-1"}}, nil, siteSet()))
	if m["siteId"] != "site-1" || m["siteName"] != "HQ" {
		t.Errorf("site = %v / %v", m["siteId"], m["siteName"])
	}
	geo, ok := m["geo"].(map[string]any)
	if !ok {
		t.Fatalf("geo = %#v, want the site's location", m["geo"])
	}
	if geo["source"] != "site" || geo["label"] != "Paris, IDF, FR" {
		t.Errorf("geo = %v", geo)
	}

	// A router naming a site that no longer exists keeps its id and reports no
	// name — not a crash, and not a name borrowed from another site.
	m = fields(t, BuildRow(Input{ID: "r-A", SiteIDs: []string{"site-gone"}}, nil, siteSet()))
	if m["siteId"] != "site-gone" || m["siteName"] != nil {
		t.Errorf("a dangling site reference gave %v / %v", m["siteId"], m["siteName"])
	}
}

// TestTheRoutersOwnPlaceBeatsItsSite — the priority order belongs to geoplace,
// and this only checks the builder hands it both tiers.
func TestTheRoutersOwnPlaceBeatsItsSite(t *testing.T) {
	in := Input{ID: "r-A", SiteIDs: []string{"site-1"}, Geo: map[string]any{
		"place": map[string]any{
			"name": "Berlin", "region": "BE", "cc": "DE", "lat": 52.52, "lon": 13.4,
		},
	}}
	geo := fields(t, BuildRow(in, nil, siteSet()))["geo"].(map[string]any)
	if geo["source"] != "manual" || geo["label"] != "Berlin, BE, DE" {
		t.Errorf("geo = %v, want the router's own place to win", geo)
	}
}

// TestTheWanAddressReachesEveryDevicesViewer. RE-AIMED 2026-10-06: this held
// the opposite, that the address was withheld without `system:settings`. The
// operator decided anyone who can open the Devices page may see a device's
// addresses, so the row has no gate to pass and must carry it whole.
func TestTheWanAddressReachesEveryDevicesViewer(t *testing.T) {
	in := Input{ID: "r-A", Geo: map[string]any{
		"auto": map[string]any{
			"name": "Hamburg", "region": "HH", "cc": "DE", "lat": 53.55, "lon": 10.0,
			"ip": "198.51.100.7", "accuracyKm": 5.0,
		},
	}}
	geo := fields(t, BuildRow(in, nil, nil))["geo"].(map[string]any)
	if geo["wanIp"] != "198.51.100.7" || geo["source"] != "auto" || geo["lat"] != 53.55 {
		t.Errorf("geo = %v, want the automatic fix with its address", geo)
	}
}

// TestThePublicIPComesFromTheSystemReading: null until the router has told
// us, then the address and how it was found.
func TestThePublicIPComesFromTheSystemReading(t *testing.T) {
	m := fields(t, BuildRow(Input{ID: "r", System: &collect.SystemPayload{}}, nil, nil))
	if m["publicIp"] != nil {
		t.Errorf("publicIp = %#v before any is known, want null", m["publicIp"])
	}
	ip := "203.0.113.9"
	m = fields(t, BuildRow(Input{ID: "r", System: &collect.SystemPayload{
		PublicIP: &ip}}, nil, nil))
	if m["publicIp"] != ip {
		t.Errorf("publicIp = %#v, want %s", m["publicIp"], ip)
	}
}

// ── the WAN address the automatic fix is derived from ────────────────────────

func TestWanIPForPrefersTheLiveValueAndStripsThePrefix(t *testing.T) {
	iface := &collect.Interface{IPs: []string{"192.0.2.9/24", "192.0.2.10/24"}}
	cases := []struct {
		name string
		live string
		wan  *collect.Interface
		want string
	}{
		{"the live value wins", "198.51.100.7/24", iface, "198.51.100.7"},
		{"the interface is the fallback", "", iface, "192.0.2.9"},
		{"a bare address is unchanged", "198.51.100.7", nil, "198.51.100.7"},
		{"nothing at all", "", nil, ""},
		{"an interface with no addresses", "", &collect.Interface{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WanIPFor(c.live, c.wan); got != c.want {
				t.Errorf("got %q, want %q — a geoip lookup wants an address, and "+
					"198.51.100.7/24 is not one", got, c.want)
			}
		})
	}
}
