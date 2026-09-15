package alertwire

import (
	"testing"

	"mikrodash/internal/collect"
)

// A DISABLED NETWATCH HOST IS NOT A DOWN ONE (#97).
//
// RouterOS does not probe a disabled host, so its status says nothing about the
// host. Now that the NetWatch page can disable one, handing on its last status
// would let a disable raise "down", or a re-enable raise "up" off a reading from
// before. It goes to the rule as `unknown`, which the rule skips without
// touching its state.
func TestADisabledNetwatchHostIsHandedOnAsNotProbed(t *testing.T) {
	got := hosts([]collect.NetwatchHost{
		{ID: "*1", Host: "198.51.100.1", Status: "down", Disabled: true},
		{ID: "*2", Host: "198.51.100.2", Status: "down"},
	})
	if len(got) != 2 {
		t.Fatalf("%d hosts, want 2", len(got))
	}
	if got[0].Status != "unknown" {
		t.Errorf("a disabled host reached the rule as %q, want unknown", got[0].Status)
	}
	if got[1].Status != "down" {
		t.Errorf("an enabled host reached the rule as %q, want its own status down", got[1].Status)
	}
}
