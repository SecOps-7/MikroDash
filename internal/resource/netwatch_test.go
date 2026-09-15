package resource

import (
	"strings"
	"testing"
)

// THE NETWATCH FORM NEVER CARRIES CODE, AND ONLY SENDS WHAT A PROBE USES (#97).

func TestNetwatchNeverOffersScriptsOrTheDNSRecordType(t *testing.T) {
	for _, f := range Netwatch.Fields {
		if strings.HasSuffix(f.ROS, "-script") {
			t.Errorf("%s is a field: a NetWatch script runs as RouterOS's system user", f.ROS)
		}
		if f.ROS == "record-type" || f.ROS == "dns-server" {
			t.Errorf("%s is a field: its values are undocumented, and a select that misses one "+
				"rewrites the probe on save", f.ROS)
		}
	}
}

func TestNetwatchProbeTypesAreTheDocumentedSix(t *testing.T) {
	want := []string{"simple", "icmp", "tcp-conn", "http-get", "https-get", "dns"}
	for _, f := range Netwatch.Fields {
		if f.Name != "type" {
			continue
		}
		if strings.Join(f.Options, ",") != strings.Join(want, ",") {
			t.Errorf("probe types %v, want %v", f.Options, want)
		}
		return
	}
	t.Fatal("the NetWatch resource has no type field")
}

func TestNetwatchSendsAPortOnlyForAProbeThatUsesOne(t *testing.T) {
	simple, errs := Netwatch.Validate(map[string]string{
		"host": "8.8.8.8", "type": "simple", "port": "53", "disabled": "false",
	}, false)
	if len(errs) > 0 {
		t.Fatalf("a simple probe failed validation: %+v", errs)
	}
	if _, has := simple.Values["port"]; has {
		t.Error("a port was sent for a simple probe, which has none")
	}
	tcp, errs := Netwatch.Validate(map[string]string{
		"host": "8.8.8.8", "type": "tcp-conn", "port": "53", "disabled": "false",
	}, false)
	if len(errs) > 0 || tcp.Values["port"] != "53" {
		t.Errorf("a tcp-conn probe's port = %q (errors %+v), want 53", tcp.Values["port"], errs)
	}
	if _, errs := Netwatch.Validate(map[string]string{"type": "simple", "disabled": "false"}, false); len(errs) == 0 {
		t.Error("a probe with no host passed validation")
	}
}
