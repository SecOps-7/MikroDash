package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// A ROUTER THAT REPORTS ITS VERSION WITHOUT A CHANNEL (RouterOS 7.25+).
//
// 7.25's changelog: "system - remove the channel label from the RouterOS
// version". Measured on CHR Test, 2026-11-10: `/system/resource/print` answered
// `7.24.5 (stable)` before the upgrade and `7.25.1` after it. Every reader here
// strips a trailing parenthetical, and stripping nothing must leave the number
// exactly as it was. Each case carries the older labelled form beside it, so a
// strip that broke on either spelling fails here.
func TestAPlainVersionIsReadAsItIs(t *testing.T) {
	for _, v := range []string{"7.25.1", "7.24.5 (stable)"} {
		want := "7.25.1"
		if v != want {
			want = "7.24.5"
		}

		// buildSystem: the dashboard's system card and its update verdict.
		p := buildSystem(routeros.Reply{"version": v}, nil,
			routeros.Reply{"latest-version": want, "status": "System is already up to date"}, nil, nil, 1000)
		if p.UpdateAvailable {
			t.Errorf("%q: an update was reported against its own version", v)
		}
		p = buildSystem(routeros.Reply{"version": v}, nil,
			routeros.Reply{"latest-version": "7.26", "status": "New version is available"}, nil, nil, 1000)
		if !p.UpdateAvailable {
			t.Errorf("%q: 7.26 was not reported as an update", v)
		}

		// parseUpdate: the Packages page's installed version.
		if got := parseUpdate(routeros.Reply{"installed-version": v}).InstalledVersion; got != want {
			t.Errorf("parseUpdate(%q) installed %q, want %q", v, got, want)
		}

		// reportIdentity: the version stored on the device record.
		var got Identity
		s := &System{onIdentity: func(id Identity) { got = id }}
		s.reportIdentity(&SystemPayload{Version: v, BoardName: "CHR"})
		if got.OSVersion != want {
			t.Errorf("reportIdentity(%q) stored %q, want %q", v, got.OSVersion, want)
		}
	}
}
