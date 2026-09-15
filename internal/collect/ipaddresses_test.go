package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// THE IP ADDRESSES PAGE DRAWS OR EDITS EVERY FIELD, SO EVERY FIELD EMITS (#97).
func TestIPAddressesFingerprintCoversEveryRenderedField(t *testing.T) {
	row := &IPAddress{
		ID: "*1", Family: "ipv4", Address: "198.51.100.1/24", Network: "198.51.100.0",
		Interface: "bridge", ActualInterface: "bridge", Comment: "lan", FromPool: "",
	}
	assertFieldsCovered(t, "IPAddress", row, map[string]string{},
		func(r any) string { return ipAddressesFingerprint([]IPAddress{*r.(*IPAddress)}) })
}

// BOTH FAMILIES, EACH SAYING WHICH IT IS, AND NOTHING WITHOUT AN ID.
func TestIPAddressesKeepsTheFamiliesApart(t *testing.T) {
	got := BuildIPAddresses(
		[]routeros.Reply{
			{".id": "*1", "address": "198.51.100.1/24", "network": "198.51.100.0", "interface": "bridge", "dynamic": "false"},
			{"address": "198.51.100.9/24", "interface": "ether1"}, // no .id: cannot be edited
			{".id": "*2", "address": "203.0.113.5/30", "interface": "ether1", "dynamic": "true", "comment": "dhcp"},
		},
		[]routeros.Reply{
			{".id": "*A", "address": "2001:db8::1/64", "interface": "bridge", "advertise": "true", "eui-64": "false", "invalid": "true"},
		},
	)
	if len(got) != 3 {
		t.Fatalf("%d addresses, want 3 (the id-less row dropped): %+v", len(got), got)
	}
	if got[0].Family != "ipv4" || got[0].Network != "198.51.100.0" || got[0].Dynamic {
		t.Errorf("first IPv4 row = %+v", got[0])
	}
	if got[1].Family != "ipv4" || !got[1].Dynamic || got[1].Comment != "dhcp" {
		t.Errorf("dynamic IPv4 row = %+v", got[1])
	}
	if got[2].Family != "ipv6" || !got[2].Advertise || got[2].EUI64 || !got[2].Invalid {
		t.Errorf("IPv6 row = %+v", got[2])
	}
}
