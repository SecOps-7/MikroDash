package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// THE NETWATCH PAGE DRAWS AND EDITS EVERY FIELD, SO EVERY FIELD EMITS (#97).
//
// The fingerprint was the id and status alone, which was right while only the
// Dashboard card read this payload. A field it leaves out is re-read after a
// save, hashes identically and is never emitted, so the page keeps showing the
// value from before the save.
func TestNetwatchFingerprintCoversEveryRenderedField(t *testing.T) {
	row := &NetwatchHost{
		ID: "*1", Host: "198.51.100.1", Type: "simple", Status: "up",
		Name: "gateway", Comment: "isp", Disabled: false, Interval: "1m",
	}
	assertFieldsCovered(t, "NetwatchHost", row, map[string]string{},
		func(r any) string { return netwatchFingerprint([]NetwatchHost{*r.(*NetwatchHost)}) })
}

func TestANetwatchHostCarriesWhetherItIsDisabledAndItsInterval(t *testing.T) {
	got := BuildNetwatch([]routeros.Reply{
		{".id": "*1", "host": "198.51.100.1", "type": "simple", "status": "up", "disabled": "true", "interval": "1m"},
		{".id": "*2", "host": "198.51.100.2", "type": "icmp", "status": "down", "disabled": "false", "interval": "10s"},
	})
	if len(got) != 2 {
		t.Fatalf("%d hosts, want 2", len(got))
	}
	if !got[0].Disabled || got[0].Interval != "1m" {
		t.Errorf("host 1: disabled=%v interval=%q, want true and 1m", got[0].Disabled, got[0].Interval)
	}
	if got[1].Disabled || got[1].Interval != "10s" {
		t.Errorf("host 2: disabled=%v interval=%q, want false and 10s", got[1].Disabled, got[1].Interval)
	}
}
