package server

import (
	"testing"

	"mikrodash/internal/db"
)

// EVERY ALERT MUST NOT GO OUT TWICE.
//
// ── THE DEFECT THIS EXISTS FOR ─────────────────────────────────────────────
//
// `SeedNotifyChannels` copies an install's configured transports into channels
// on first start, so afterwards the SAME Telegram credentials exist both as
// flat settings and as a channel. The first version of the channel send path
// appended channel recipients to the legacy ones, which meant every alert was
// delivered twice to the same destination — once as the `_install` recipient and
// once as its own channel. Nothing in the suite noticed, because both halves
// worked perfectly.
//
// So `haveChannels` is the switch between the two models, and it is the only
// thing standing between an operator and a duplicate of every notification.
func TestChannelsReplaceTheLegacyRecipientsRatherThanJoiningThem(t *testing.T) {
	s, _, _ := scopedRoutersServer(t)
	if s.auditDB == nil {
		t.Skip("this harness has no database")
	}

	// The harness builds its database from hand-written DDL, so the channels
	// table has to be migrated in.
	if _, err := s.auditDB.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Before any channel exists the legacy path is live — that is the window
	// between an upgrade and the seed, and an install that never adopts
	// channels at all.
	if s.haveChannels() {
		t.Fatal("a fresh install reported that it already has channels")
	}

	if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{
		ID: "c1", Owner: db.InstallOwner, Name: "Telegram", Kind: "webhook",
		Enabled: 1, Config: `{"urls":["tgram://1:A/2"]}`,
		Events: `["ping_loss"]`, Routers: `[]`, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// And once one exists, it is not.
	if !s.haveChannels() {
		t.Error("a channel was created and the install still reported none — " +
			"the legacy recipients would keep sending, and every alert would " +
			"be delivered twice to the same destination")
	}
}

// AN UNREADABLE TABLE DEGRADES TO THE OLD BEHAVIOUR, NOT TO SILENCE. If the
// count cannot be taken, the legacy recipients must keep working: an install
// that stops notifying is worse than one that notifies the old way.
func TestAnUnreadableChannelTableKeepsTheLegacyPath(t *testing.T) {
	s := &Server{}
	if s.haveChannels() {
		t.Error("a server with no database claimed to have channels, which would " +
			"switch off the legacy recipients and deliver nothing at all")
	}
}
