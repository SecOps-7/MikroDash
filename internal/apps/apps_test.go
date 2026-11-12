package apps

import "testing"

// TestRealDestinationsAreNamed runs the embedded catalogue against hostnames,
// AS numbers and ports as a home network produces them.
func TestRealDestinationsAreNamed(t *testing.T) {
	c := Default()
	for _, tc := range []struct {
		names         []string
		asn           uint
		proto         string
		port          int
		want, wantVia string
	}{
		{[]string{"us04web.zoom.us"}, 0, "tcp", 443, "zoom", ViaName},
		{[]string{"teams.microsoft.com"}, 8075, "tcp", 443, "teams", ViaName},
		{[]string{"xsts.auth.xboxlive.com"}, 8075, "tcp", 443, "xbox", ViaName},
		{[]string{"rr3---sn-abc.googlevideo.com"}, 15169, "tcp", 443, "youtube", ViaName},
		{[]string{"mail.google.com", "www.google.com"}, 15169, "tcp", 443, "gmail", ViaName},
		{[]string{"www.google.com", "mail.google.com"}, 15169, "tcp", 443, "gmail", ViaName},
		{[]string{"nflxvideo.net"}, 0, "tcp", 443, "netflix", ViaName},
		{[]string{"steamcdn-a.akamaihd.net", "api.steampowered.com"}, 0, "tcp", 443, "steam", ViaName},
		{[]string{"WWW.Cloudflare.COM."}, 0, "tcp", 443, "cloudflare", ViaName},
		{[]string{"hulu.com"}, 0, "tcp", 443, "hulu", ViaName},
		{[]string{"www.showmax.com"}, 0, "tcp", 443, "showmax", ViaName},
		// No name: the port names it before the network owner does.
		{nil, 15169, "udp", 53, "dns", ViaPort},
		{nil, 8075, "udp", 3074, "xbox", ViaPort},
		{nil, 0, "udp", 27015, "steam", ViaPort},
		// No name, no telling port: the network owner.
		{nil, 30103, "tcp", 443, "zoom", ViaASN},
		{nil, 13335, "tcp", 443, "cloudflare", ViaASN},
		{nil, 32934, "tcp", 443, "meta", ViaASN},
		{nil, 8075, "tcp", 443, "microsoft", ViaASN},
		// A name the catalogue does not know does not block the fallbacks.
		{[]string{"example.org"}, 2906, "tcp", 443, "netflix", ViaASN},
	} {
		m := c.Classify(tc.names, tc.asn, tc.proto, tc.port)
		if m.App == nil || m.App.ID != tc.want || m.Via != tc.wantVia {
			got := "nothing"
			if m.App != nil {
				got = m.App.ID + " via " + m.Via
			}
			t.Errorf("%v AS%d %s/%d: %s, want %s via %s", tc.names, tc.asn, tc.proto, tc.port, got, tc.want, tc.wantVia)
		}
	}
	if m := c.Classify([]string{"example.org"}, 64512, "tcp", 443); m.App != nil {
		t.Errorf("an unknown destination was named %s", m.App.ID)
	}
	// A suffix must match at a label boundary: notzoom.us is not Zoom.
	if m := c.Classify([]string{"notzoom.us"}, 0, "tcp", 443); m.App != nil && m.App.ID == "zoom" {
		t.Error("notzoom.us matched zoom.us")
	}
}

// TestTheCatalogueIsWhole: every app can be drawn and every category is known.
func TestTheCatalogueIsWhole(t *testing.T) {
	c := Default()
	if len(c.Apps()) < 100 {
		t.Fatalf("the embedded catalogue has %d apps", len(c.Apps()))
	}
	for _, a := range c.Apps() {
		if a.ID == "" || a.Name == "" || len(a.Color) != 7 || a.Color[0] != '#' {
			t.Errorf("%+v cannot be drawn", a)
		}
		if a.Logo != "" && a.Logo != "mask" && a.Logo != "image" {
			t.Errorf("%s: logo kind %q", a.ID, a.Logo)
		}
	}
	if _, ok := c.App("teams"); !ok {
		t.Error("App(teams) found nothing")
	}
}
