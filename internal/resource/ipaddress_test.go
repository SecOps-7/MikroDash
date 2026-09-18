package resource

import (
	"testing"
)

// AN ADDRESS GOES TO ITS OWN FAMILY'S MENU, AND ONLY DOCUMENTED FIELDS ARE SENT (#97).

func TestAnAddressOfTheOtherFamilyIsRefused(t *testing.T) {
	if _, errs := IPAddress.Validate(map[string]string{
		"address": "2001:db8::1/64", "interface": "bridge", "disabled": "false",
	}, false); len(errs) == 0 {
		t.Error("an IPv6 address was accepted for /ip/address")
	}
	if _, errs := IPv6Address.Validate(map[string]string{
		"address": "198.51.100.1/24", "interface": "bridge", "disabled": "false",
	}, false); len(errs) == 0 {
		t.Error("an IPv4 address was accepted for /ipv6/address")
	}
	if _, errs := IPAddress.Validate(map[string]string{
		"address": "198.51.100.1/24", "interface": "bridge", "disabled": "false",
	}, false); len(errs) > 0 {
		t.Errorf("a plain IPv4 address was refused: %+v", errs)
	}
	if _, errs := IPv6Address.Validate(map[string]string{
		"address": "2001:db8::1/64", "interface": "bridge", "disabled": "false",
	}, false); len(errs) > 0 {
		t.Errorf("a plain IPv6 address was refused: %+v", errs)
	}
}

func TestADynamicAddressIsReadOnly(t *testing.T) {
	for _, r := range []*Resource{IPAddress, IPv6Address} {
		if !r.ReadOnlyWhen(map[string]string{"address": "198.51.100.1/24", "dynamic": "true"}) {
			t.Errorf("%s: a dynamic address is editable", r.Key)
		}
		if r.ReadOnlyWhen(map[string]string{"address": "198.51.100.1/24", "dynamic": "false"}) {
			t.Errorf("%s: a static address is read-only", r.Key)
		}
	}
}

// A derived property may be SHOWN — the IP Addresses page has always shown the
// network and the dynamic and invalid flags — but only as a Display field, which
// Validate drops, so it can never be sent. Re-aimed on 2026-09-18, when the page
// became an area and those columns had to become fields: before Display existed,
// "not a field at all" was the only way to say "never sent".
func TestAddressFormsOfferNoDerivedOrUndocumentedField(t *testing.T) {
	banned := map[string]bool{"network": true, "broadcast": true, "from-pool": true,
		"from-pool-policy": true, "no-dad": true, "auto-link-local": true,
		"dynamic": true, "invalid": true}
	shown := 0
	for _, r := range []*Resource{IPAddress, IPv6Address} {
		sample := map[string]string{"address": "198.51.100.1/24", "interface": "bridge"}
		if r == IPv6Address {
			sample["address"] = "2001:db8::1/64"
		}
		for _, f := range r.Fields {
			if !banned[f.ROS] {
				continue
			}
			if !f.Display {
				t.Errorf("%s offers %s to be written, which RouterOS derives or does not document", r.Key, f.ROS)
				continue
			}
			shown++
			sample[f.Name] = "true"
		}
		// AND DISPLAY MEANS WHAT IT SAYS: nothing derived survives validation.
		v, errs := r.Validate(sample, false)
		if len(errs) > 0 {
			t.Fatalf("%s: the sample was refused: %+v", r.Key, errs)
		}
		ros := map[string]string{}
		for _, f := range r.Fields {
			ros[f.Name] = f.ROS
		}
		for k := range v.Values {
			if banned[ros[k]] {
				t.Errorf("%s: validation kept %s, so it would be sent", r.Key, ros[k])
			}
		}
	}
	if shown == 0 {
		t.Fatal("no derived field is shown, so the validation half of this test checks nothing")
	}
}
