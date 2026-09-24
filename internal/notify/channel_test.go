package notify

import "testing"

func webhookSpec(events, routers string) ChannelSpec {
	return DecodeChannel("c1", "Ops", KindWebhook, true,
		`{"urls":["ntfy://ntfy.example.net/ops"]}`, events, routers)
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
		`{"urls":["ntfy://ntfy.example.net/ops"]}`, `["ping_loss"]`, `[]`)
	if c.Wants("ping_loss", "r1") {
		t.Error("a disabled channel still wanted its events")
	}
}

// A CORRUPT COLUMN LOSES ONE CHANNEL, NOT ALL OF THEM. The same choice
// usernotify_api.go makes when a credential will not decrypt.
func TestUnparseableColumnsDegradeQuietly(t *testing.T) {
	c := DecodeChannel("c1", "Ops", KindWebhook, true, `{not json`, `{not json`, `{not json`)
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
		`["ping_loss"]`, `[]`)

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
		`{"host":"mail.example.net","to":"ops@example.net"}`, `[]`, `[]`)
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
			DecodeChannel("c", "n", KindWebhook, true, `{"urls":[]}`, `[]`, `[]`), false},
		{"webhook with only blanks",
			DecodeChannel("c", "n", KindWebhook, true, `{"urls":["","  "]}`, `[]`, `[]`), false},
		{"smtp with host and recipient",
			DecodeChannel("c", "n", KindSMTP, true,
				`{"host":"mail.example.net","to":"ops@example.net"}`, `[]`, `[]`), true},
		{"smtp with no recipient",
			DecodeChannel("c", "n", KindSMTP, true,
				`{"host":"mail.example.net"}`, `[]`, `[]`), false},
		{"unknown kind", DecodeChannel("c", "n", "carrier-pigeon", true, `{}`, `[]`, `[]`), false},
	}
	for _, c := range cases {
		if got := c.spec.Deliverable(); got != c.want {
			t.Errorf("%s: Deliverable() = %v, want %v", c.name, got, c.want)
		}
	}
}
