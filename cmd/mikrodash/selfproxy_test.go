package main

import "testing"

// A `-node` THAT POINTS AT OUR OWN LISTENER IS REFUSED.
//
// ── THE DEFECT THIS EXISTS FOR HAPPENED DURING THE CUTOVER ────────────────
//
// `-node` defaulted to `http://127.0.0.1:3081`, which was correct for as long
// as Node owned that port. At cutover the app binds :3081 itself, so the
// default made it proxy every un-ported route to itself — and because the pool
// and the retention sweep are both gated on `standalone`, which means "no
// -node", they silently stayed off too. Three subsystems wrong from one stale
// default, visible only in a startup line nobody had reason to re-read.
//
// The default is now empty. This makes the loop unrepresentable rather than
// merely unlikely, because a future operator may still pass -node by hand.
func TestAProxyTargetThatIsOurselfIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, listen, node string
		wantErr            bool
	}{
		// The exact cutover case.
		{"the cutover default against :3081", ":3081", "http://127.0.0.1:3081", true},
		{"localhost spelled out", ":3081", "http://localhost:3081", true},
		{"the unspecified host", ":3081", "http://0.0.0.0:3081", true},
		{"a host on the listen interface", "127.0.0.1:3081", "http://127.0.0.1:3081", true},
		// HOST AND PORT, not string equality: these two spell the same endpoint
		// differently, and a string compare would pass them both.
		{"implicit port 80 against :80", ":80", "http://127.0.0.1", true},

		// Legitimate: a DIFFERENT process.
		{"standalone", ":3081", "", false},
		{"standalone with spaces", ":3081", "   ", false},
		{"a different port", ":3085", "http://127.0.0.1:3081", false},
		{"a different machine", ":3081", "http://10.0.0.9:3081", false},
		// Same port, but not us — another host entirely.
		{"another host on the same port", ":3081", "http://mikrodash.lan:3081", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := mustNotProxyToSelf(c.listen, c.node)
			if c.wantErr && err == nil {
				t.Errorf("listen=%q node=%q was ACCEPTED; every un-ported route would "+
					"proxy into this same process", c.listen, c.node)
			}
			if !c.wantErr && err != nil {
				t.Errorf("listen=%q node=%q was refused (%v), but it names a different "+
					"process — refusing it would block a legitimate proxy setup",
					c.listen, c.node, err)
			}
		})
	}
}
