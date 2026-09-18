package guard

import "testing"

var apiSSL = ServiceRow{Name: "api-ssl", Port: "8729"}

func TestTheServiceMikroDashUsesCannotBeCutOff(t *testing.T) {
	self := []string{"198.51.100.5"}
	for code, after := range map[string]ServiceRow{
		"service-disable": {Name: "api-ssl", Port: "8729", Disabled: true},
		"service-port":    {Name: "api-ssl", Port: "8730"},
		"service-vrf":     {Name: "api-ssl", Port: "8729", VRF: "mgmt"},
		"service-address": {Name: "api-ssl", Port: "8729", Address: "192.0.2.0/24"},
	} {
		v := CheckServiceEdit("api-ssl", self, true, apiSSL, after)
		if !v.Refused() || v.Code != code {
			t.Errorf("%s: %+v", code, v)
		}
		if v.Fingerprint != "" {
			t.Errorf("%s: a refusal carries a fingerprint, so something could acknowledge it", code)
		}
	}
	// Unknown source: an address restriction cannot be shown to admit us.
	if v := CheckServiceEdit("api-ssl", nil, false, apiSSL, ServiceRow{Name: "api-ssl", Port: "8729", Address: "198.51.100.0/24"}); !v.Refused() || v.Code != "service-address-unknown" {
		t.Errorf("an address restriction with our source unknown: %+v", v)
	}
}

// THE CONTROLS: each is a change that leaves MikroDash connected.
func TestWhatLeavesUsConnectedIsAllowed(t *testing.T) {
	self := []string{"198.51.100.5"}
	for name, c := range map[string]struct {
		ours          string
		before, after ServiceRow
		resolved      bool
	}{
		"an address list that admits us":    {"api-ssl", apiSSL, ServiceRow{Name: "api-ssl", Port: "8729", Address: "192.0.2.0/24, 198.51.100.0/24"}, true},
		"clearing the address list":         {"api-ssl", ServiceRow{Name: "api-ssl", Port: "8729", Address: "198.51.100.0/24"}, apiSSL, false},
		"enabling it":                       {"api-ssl", ServiceRow{Name: "api-ssl", Port: "8729", Disabled: true}, apiSSL, true},
		"another service: disabling telnet": {"api-ssl", ServiceRow{Name: "telnet", Port: "23"}, ServiceRow{Name: "telnet", Port: "23", Disabled: true}, true},
		// The other API service is not ours when we speak TLS.
		"plain api while we use api-ssl": {"api-ssl", ServiceRow{Name: "api", Port: "8728"}, ServiceRow{Name: "api", Port: "8728", Disabled: true}, true},
	} {
		if v := CheckServiceEdit(c.ours, self, c.resolved, c.before, c.after); v.Level != "none" {
			t.Errorf("%s: %+v", name, v)
		}
	}
	// And the reverse: when we speak plain api, api is the protected one.
	if v := CheckServiceEdit("api", self, true, ServiceRow{Name: "api", Port: "8728"}, ServiceRow{Name: "api", Port: "8728", Disabled: true}); !v.Refused() {
		t.Errorf("disabling api while MikroDash uses it: %+v", v)
	}
}

func TestTheCertificateApiSSLPresentsCannotBeRemoved(t *testing.T) {
	v := CheckCertificateRemove(true, "chr-api", "chr-api")
	if !v.Refused() || v.Code != "certificate-in-use" || v.Detail["value"] != "chr-api" {
		t.Fatalf("removing api-ssl's certificate over TLS: %+v", v)
	}
	// CONTROLS: another certificate; plain api; api-ssl presenting none.
	for name, c := range map[string]struct {
		tls        bool
		used, name string
	}{
		"another certificate":        {true, "chr-api", "old-ca"},
		"MikroDash speaks plain api": {false, "chr-api", "chr-api"},
		"api-ssl names none":         {true, "none", "none"},
		"api-ssl names nothing":      {true, "", "chr-api"},
	} {
		if v := CheckCertificateRemove(c.tls, c.used, c.name); v.Level != "none" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}
