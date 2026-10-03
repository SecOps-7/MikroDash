package server

import (
	"slices"
	"testing"

	"mikrodash/internal/resource"
)

// TestTheLockoutCheckIsToldWhatItCannotEvaluate. The firewall form sets about
// sixty properties and the lockout guard models six; the rest of the MATCHES
// that are set reach it as "Label: value", so its warning can name them. What a
// rule does (the Action tab) is not a match, and the six it models are not
// repeated.
func TestTheLockoutCheckIsToldWhatItCannotEvaluate(t *testing.T) {
	r := fwRuleFrom(resource.FWFilter, map[string]string{
		"chain": "input", "action": "drop", "srcAddress": "10.0.0.0/8",
		"srcAddressList": "blocked", "connectionState": "new", "time": "8h-17h",
		"logPrefix": "DROP", "comment": "x", "inInterfaceList": "",
	})
	want := []string{"Connection State: new", "Src. Address List: blocked", "Time: 8h-17h"}
	if !slices.Equal(r.Unmodelled, want) {
		t.Errorf("unmodelled = %q, want %q", r.Unmodelled, want)
	}
	if r.SrcAddress != "10.0.0.0/8" || r.Chain != "input" {
		t.Errorf("the modelled fields were not carried: %+v", r)
	}
}
