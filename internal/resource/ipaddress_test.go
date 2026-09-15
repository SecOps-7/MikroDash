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

func TestAddressFormsOfferNoDerivedOrUndocumentedField(t *testing.T) {
	banned := map[string]bool{"network": true, "broadcast": true, "from-pool": true,
		"from-pool-policy": true, "no-dad": true, "auto-link-local": true}
	for _, r := range []*Resource{IPAddress, IPv6Address} {
		for _, f := range r.Fields {
			if banned[f.ROS] {
				t.Errorf("%s offers %s, which RouterOS derives or does not document", r.Key, f.ROS)
			}
		}
	}
}
