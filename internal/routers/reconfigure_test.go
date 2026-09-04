package routers

import (
	"testing"
	"time"
)

// TestAChangedCredentialRebuildsAPooledSession.
//
// ── THE POOL DIALLED THE CREDENTIALS IT WAS BUILT WITH, FOR EVER ───────────
//
// `SyncPool` decides from the ID set, which is the right question and not the
// only one: a session already tracked kept the `RouterConfig` captured in
// `build`, and nothing re-read it. So correcting a password — or moving a router
// to a new address, or turning TLS on — changed routers.json and nothing else.
// The pool went on presenting the old credential every five seconds, and every
// attempt is a rejected login in the router's own log. Issue #124.
func TestAChangedCredentialRebuildsAPooledSession(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	first := cfg("a")
	first.Password = "old-one"
	p.Sync([]RouterConfig{first}, nil)
	waitFor(t, "the first connection", func() bool { return d.count() >= 1 })
	if got := d.lastCfg().Password; got != "old-one" {
		t.Fatalf("dialled with %q before the change", got)
	}

	changed := cfg("a")
	changed.Password = "corrected"
	act := p.Sync([]RouterConfig{changed}, nil)
	if len(act.Start) != 1 || act.Start[0] != "a" || len(act.Stop) != 1 {
		t.Fatalf("a changed credential decided %+v, want the router rebuilt", act)
	}
	waitFor(t, "a redial with the corrected credential", func() bool {
		return d.lastCfg().Password == "corrected"
	})
}

// TestOnlyThePasswordChangedRebuilds — the case above changes only the
// password too, but this one states it as the property, because it is the field
// a comparison is most likely to be missing: every other one is visible on the
// Devices page, and a wrong password looks exactly like a router that is down.
func TestOnlyThePasswordChangedRebuilds(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	base := cfg("a")
	base.Password = "stale"
	p.Sync([]RouterConfig{base}, nil)
	waitFor(t, "the first connection", func() bool { return d.count() >= 1 })

	only := base
	only.Password = "retyped"
	if act := p.Sync([]RouterConfig{only}, nil); len(act.Start) != 1 {
		t.Fatalf("a password-only change decided %+v", act)
	}
	waitFor(t, "a redial with the retyped credential", func() bool {
		return d.lastCfg().Password == "retyped"
	})
}

// TestAnUnchangedRouterIsNotRebuilt is the other direction, and it is the half
// that keeps the fix honest: comparing something that changes on every sync —
// or comparing nothing and always rebuilding — would drop and re-open every
// connection in the fleet on every edit to any router.
func TestAnUnchangedRouterIsNotRebuilt(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil)
	defer p.Close()

	p.Sync([]RouterConfig{cfg("a")}, nil)
	waitFor(t, "the first connection", func() bool { return d.count() >= 1 })
	before := d.count()

	// A LABEL CHANGE IS NOT A CONNECTION CHANGE, and neither is a collection
	// block: both alter what the session does, not where it dials.
	renamed := cfg("a")
	renamed.Label = "Renamed"
	renamed.DefaultIf = "ether2"
	act := p.Sync([]RouterConfig{renamed}, nil)
	if len(act.Start) != 0 || len(act.Stop) != 0 {
		t.Errorf("a rename decided %+v; the connection was dropped for nothing", act)
	}
	time.Sleep(50 * time.Millisecond)
	if d.count() != before {
		t.Errorf("redialled %d extra times after a rename", d.count()-before)
	}
}
