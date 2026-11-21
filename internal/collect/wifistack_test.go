package collect

import (
	"errors"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// stackReader is a router with both wireless menus: /interface/wifi and
// /interface/wireless answer with the rows held here (none, for a router with
// no radios of its own), the legacy security profiles hold "default", and
// every CAPsMAN v1 menu is absent.
type stackReader struct {
	wifi, wireless []routeros.Reply
}

func (s *stackReader) Connected() bool { return true }

func (s *stackReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	switch cmd.Path {
	case wifiIfaceCmd.Path:
		return s.wifi, nil
	case wlIfaceCmd.Path:
		return s.wireless, nil
	case wlProfileCmd.Path:
		return []routeros.Reply{{".id": "*1", "name": "default", "mode": "none",
			"authentication-types": "", "default": "true"}}, nil
	}
	if strings.HasPrefix(cmd.Path, "/caps-man/") {
		return nil, errors.New("no such command prefix")
	}
	return nil, nil
}

// TestBothEmptyStacksDoNotAlternate is issue #166. A router where both
// /interface/wifi and /interface/wireless exist and both list nothing (an
// RB5009 with no radios of its own) had its stack flip on every load:
// whichever menu answered second when the first was empty was latched, and the
// order came from the previous latch. The Security Profiles card, shown only
// for the legacy stack, appeared and disappeared with it, and each flip moved
// the subscription to the other menu, which made the next load due at once.
func TestBothEmptyStacksDoNotAlternate(t *testing.T) {
	var emits int
	emit := hub.NewRelay(func(string, hub.Named, any) { emits++ })
	w := NewWifi(&stackReader{}, emit, 10000)
	var stacks []string
	for i := 0; i < 6; i++ {
		w.RefreshNow()
		stacks = append(stacks, w.Last().Stack)
	}
	for _, s := range stacks[1:] {
		if s != stacks[0] {
			t.Fatalf("the stack alternated across loads: %v (the Security Profiles card flickers with it)", stacks)
		}
	}
	if emits != 1 {
		t.Errorf("%d payloads emitted for an unchanged router; want 1", emits)
	}
}

// TestALegacyRouterStillLatchesTheLegacyStack is the control: a router with
// a legacy interface of its own keeps the legacy stack, and its security
// profiles with it, on every load.
func TestALegacyRouterStillLatchesTheLegacyStack(t *testing.T) {
	r := &stackReader{wireless: []routeros.Reply{{".id": "*1", "name": "wlan1", "ssid": "Home",
		"mode": "ap-bridge", "band": "2ghz-b/g/n", "disabled": "false", "running": "true",
		"security-profile": "default"}}}
	w := NewWifi(r, hub.NewRelay(func(string, hub.Named, any) {}), 10000)
	for i := 0; i < 4; i++ {
		w.RefreshNow()
		p := w.Last()
		if p.Stack != "wireless" || len(p.SecProfiles) != 1 {
			t.Fatalf("load %d: stack %q with %d security profiles; want the legacy stack and its profile", i, p.Stack, len(p.SecProfiles))
		}
	}
}
