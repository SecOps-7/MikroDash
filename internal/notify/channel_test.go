package notify

import "testing"

func webhookSpec(events, routers string) ChannelSpec {
	return DecodeChannel("c1", "Ops", KindWebhook, true,
		`{"urls":["ntfy://ntfy.example.net/ops"]}`, events, routers, `[]`, `{}`)
}

// EMPTY ROUTERS MEANS EVERY ROUTER, and empty events means none. The two
// defaults are deliberately opposite: a channel created without touching the
// router picker should cover the fleet, while one that somehow lost its event
// list must go quiet rather than page somebody about everything.
func TestTheTwoEmptyDefaultsAreOpposite(t *testing.T) {
	all := webhookSpec(`["ping_loss"]`, `[]`)
	if !all.Wants("ping_loss", "any-router") {
		t.Error("an empty router list did not mean every router")
	}
	none := webhookSpec(`[]`, `[]`)
	if none.Wants("ping_loss", "any-router") {
		t.Error("an empty event list subscribed to an event")
	}
}

func TestAChannelOnlyWantsItsOwnEvents(t *testing.T) {
	c := webhookSpec(`["ping_loss","high_cpu"]`, `[]`)
	if !c.Wants("high_cpu", "r1") {
		t.Error("a subscribed event was refused")
	}
	if c.Wants("host_down", "r1") {
		t.Error("an unsubscribed event was accepted")
	}
}

func TestAScopedChannelOnlyWantsItsOwnRouters(t *testing.T) {
	c := webhookSpec(`["ping_loss"]`, `["r1","r2"]`)
	if !c.Wants("ping_loss", "r2") {
		t.Error("a router in the scope was refused")
	}
	if c.Wants("ping_loss", "r3") {
		t.Error("a router outside the scope was accepted — the whole point of the picker")
	}
}

func TestADisabledChannelWantsNothing(t *testing.T) {
	c := DecodeChannel("c1", "Ops", KindWebhook, false,
		`{"urls":["ntfy://ntfy.example.net/ops"]}`, `["ping_loss"]`, `[]`, `[]`, `{}`)
	if c.Wants("ping_loss", "r1") {
		t.Error("a disabled channel still wanted its events")
	}
}

// A CORRUPT COLUMN LOSES ONE CHANNEL, NOT ALL OF THEM. The same choice
// usernotify_api.go makes when a credential will not decrypt.
func TestUnparseableColumnsDegradeQuietly(t *testing.T) {
	c := DecodeChannel("c1", "Ops", KindWebhook, true, `{not json`, `{not json`, `{not json`, `{not json`, `{not json`)
	if c.ID != "c1" || c.Kind != KindWebhook {
		t.Error("a corrupt column lost the fields that did parse")
	}
	// And it wants nothing, because its event list did not survive — silence is
	// the safe end of that failure.
	if c.Wants("ping_loss", "r1") {
		t.Error("a channel with an unparseable event list still wanted an event")
	}
}

// AN SMTP CHANNEL IS MAPPED ONTO THE FLAT KEYS `Send` ALREADY READS, so it is
// delivered by internal/mailer rather than by a second SMTP implementation.
func TestAnSMTPChannelBecomesTransportSettings(t *testing.T) {
	c := DecodeChannel("c2", "Ops mail", KindSMTP, true,
		`{"host":"mail.example.net","port":465,"secure":true,"user":"u","pass":"p",
		  "from":"md@example.net","to":"ops@example.net"}`,
		`["ping_loss"]`, `[]`, `[]`, `{}`)

	want := map[string]any{
		"smtpEnabled": true, "smtpHost": "mail.example.net", "smtpPort": "465",
		"smtpSecure": true, "smtpUser": "u", "smtpPass": "p",
		"smtpFrom": "md@example.net", "smtpTo": "ops@example.net",
	}
	for k, v := range want {
		if c.Settings[k] != v {
			t.Errorf("Settings[%q] = %v, want %v", k, c.Settings[k], v)
		}
	}
	// AND IT IS RECOGNISED AS CONFIGURED by the same helper the dispatcher's
	// guard uses, or the channel would be built and then refused as empty.
	if !HasConfigured(c.Settings) {
		t.Error("an SMTP channel did not read as configured")
	}
}

// A MISSING PORT IS 587, the submission port, matching mailer.DefaultPort. Zero
// would be sent as a literal port 0 and fail to connect with nothing to explain
// it.
func TestAnSMTPChannelWithoutAPortGetsTheDefault(t *testing.T) {
	c := DecodeChannel("c2", "M", KindSMTP, true,
		`{"host":"mail.example.net","to":"ops@example.net"}`, `[]`, `[]`, `[]`, `{}`)
	if c.Settings["smtpPort"] != "587" {
		t.Errorf("smtpPort = %v, want 587", c.Settings["smtpPort"])
	}
}

// DELIVERABLE IS ABOUT HAVING SOMEWHERE TO SEND, and is what stops a
// half-configured channel reporting successful delivery of nothing.
func TestDeliverableNeedsSomewhereToSend(t *testing.T) {
	cases := []struct {
		name string
		spec ChannelSpec
		want bool
	}{
		{"webhook with a url", webhookSpec(`[]`, `[]`), true},
		{"webhook with no urls",
			DecodeChannel("c", "n", KindWebhook, true, `{"urls":[]}`, `[]`, `[]`, `[]`, `{}`), false},
		{"webhook with only blanks",
			DecodeChannel("c", "n", KindWebhook, true, `{"urls":["","  "]}`, `[]`, `[]`, `[]`, `{}`), false},
		{"smtp with host and recipient",
			DecodeChannel("c", "n", KindSMTP, true,
				`{"host":"mail.example.net","to":"ops@example.net"}`, `[]`, `[]`, `[]`, `{}`), true},
		{"smtp with no recipient",
			DecodeChannel("c", "n", KindSMTP, true,
				`{"host":"mail.example.net"}`, `[]`, `[]`, `[]`, `{}`), false},
		{"unknown kind", DecodeChannel("c", "n", "carrier-pigeon", true, `{}`, `[]`, `[]`, `[]`, `{}`), false},
	}
	for _, c := range cases {
		if got := c.spec.Deliverable(); got != c.want {
			t.Errorf("%s: Deliverable() = %v, want %v", c.name, got, c.want)
		}
	}
}

// THE PER-INTERFACE FILTER, which narrows Interface Up/Down to particular kinds
// of interface.
//
// ── THE TWO WAYS TO GET IT WRONG ARE BOTH SILENT ──────────────────────────
//
// Reading an empty list as "no types" silences every interface alert on every
// channel nobody has narrowed, which is all of them after an upgrade. Reading an
// empty `ifaceType` as something to match silences CPU, ping and BGP alerts,
// which have no interface at all — a filter about interfaces quietly switching
// off events that are not about interfaces.
func TestWhichInterfaceTypesAChannelIsToldAbout(t *testing.T) {
	spec := func(list string) ChannelSpec {
		return DecodeChannel("c", "n", KindWebhook, true,
			`{"urls":["tgram://t/1"]}`, `["interface_down"]`, `[]`, list, `{}`)
	}
	for _, tc := range []struct {
		name string
		list string
		kind string
		want bool
	}{
		{"an empty list covers every type", `[]`, "ether", true},
		{"and every other type too", `[]`, "vlan", true},
		{"a narrowed channel takes what it asked for", `["ether","wlan"]`, "ether", true},
		{"and refuses what it did not", `["ether","wlan"]`, "bridge", false},
		// THE ONE THAT WOULD SILENCE UNRELATED ALERTS. A CPU alert carries no
		// interface type, and a channel narrowed to Ethernet must still get it.
		{"an alert with no interface is never narrowed", `["ether"]`, "", true},
		{"even by a channel narrowed to one type", `["vlan"]`, "", true},
		// A list that is not JSON decodes to nothing, which means "all" — the
		// safe direction. A corrupt row must not silence a channel.
		{"an unreadable list covers everything", `{not json`, "bridge", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spec(tc.list).WantsIface(tc.kind); got != tc.want {
				t.Errorf("WantsIface(%q) with %s = %v, want %v", tc.kind, tc.list, got, tc.want)
			}
		})
	}
}

// HOW LOUD A CHANNEL IS: its own thresholds, and the cooldown that goes with
// them.
//
// ── THE TWO SILENT MISTAKES ───────────────────────────────────────────────
//
// Comparing an event that carries no number silences every alert that is not a
// measurement — an interface going down has a Value of zero, and zero is under
// every threshold. And comparing a RESOLUTION suppresses every all-clear, since
// a recovery's value is below the threshold by definition, leaving a channel
// believing an alert is still open for ever.
func TestHowLoudAChannelIs(t *testing.T) {
	spec := func(tuning string) ChannelSpec {
		return DecodeChannel("c", "n", KindWebhook, true,
			`{"urls":["tgram://t/1"]}`, `["high_cpu"]`, `[]`, `[]`, tuning)
	}
	quiet := `{"cpu":95,"pingLoss":80,"cooldownSec":300}`

	for _, tc := range []struct {
		name   string
		tuning string
		event  string
		value  float64
		up     bool
		want   bool
	}{
		{"at its CPU threshold", quiet, "high_cpu", 95, false, true},
		{"above it", quiet, "high_cpu", 99, false, true},
		{"below it", quiet, "high_cpu", 80, false, false},
		{"at its ping threshold", quiet, "ping_loss", 80, false, true},
		{"below it", quiet, "ping_loss", 60, false, false},
		// AN EVENT WITH NO NUMBER IS NEVER NARROWED.
		{"an interface has no percentage", quiet, "interface_down", 0, false, true},
		{"nor does a backup", quiet, "backup_fail", 0, false, true},
		// A RECOVERY IS ALWAYS DELIVERED, whatever it measures.
		{"a recovery below the threshold still arrives", quiet, "high_cpu", 3, true, true},
		{"and so does a ping recovery", quiet, "ping_loss", 0, true, true},
		// AN UNTUNED CHANNEL GETS THE OLD INSTALL DEFAULTS, so it behaves as the
		// whole install did before any of this was per-channel.
		{"untuned: 90 is the CPU default", `{}`, "high_cpu", 90, false, true},
		{"untuned: 89 is under it", `{}`, "high_cpu", 89, false, false},
		{"untuned: 100 is the ping default", `{}`, "ping_loss", 100, false, true},
		// A ZERO IS "NOT SET", not "alert on everything". A stored zero and an
		// absent key are indistinguishable in the row, and the reading that
		// alerts on every router at every poll cannot be what was meant.
		{"a zero threshold reads as the default", `{"cpu":0}`, "high_cpu", 50, false, false},
		{"a corrupt row reads as the defaults", `{not json`, "high_cpu", 95, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spec(tc.tuning).WantsValue(tc.event, tc.value, tc.up); got != tc.want {
				t.Errorf("WantsValue(%q, %v, up=%v) with %s = %v, want %v",
					tc.event, tc.value, tc.up, tc.tuning, got, tc.want)
			}
		})
	}

	// THE COOLDOWN COMES BACK AS A NUMBER, and a zero means the default rather
	// than "notify on every evaluation" — which would turn a flapping interface
	// into a message per poll.
	if got := spec(quiet).Tuning.CooldownSec; got != 300 {
		t.Errorf("cooldown = %d, want 300", got)
	}
	if got := spec(`{"cooldownSec":0}`).Tuning.CooldownSec; got != DefaultTuning.CooldownSec {
		t.Errorf("a zero cooldown = %d, want the default %d", got, DefaultTuning.CooldownSec)
	}
}
