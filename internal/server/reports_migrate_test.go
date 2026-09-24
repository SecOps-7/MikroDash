package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/notify"
	"mikrodash/internal/store"
)

// CARRYING EVERY SCHEDULE'S RECIPIENT LIST ONTO A CHANNEL.
//
// ── WHY THIS IS TESTED HARDER THAN THE REST ───────────────────────────────
//
// It is the one path in this change that touches OPERATOR DATA and cannot be
// re-run: the `recipients` column is dropped at the end, so a list that is
// mis-carried is a list that is gone. Every other mistake here is a page that
// looks wrong and can be fixed by editing it.
//
// It also cannot be exercised by hand on a real install, for the same reason —
// once it has run, the inputs no longer exist. So it is exercised here, against
// a real database, with the rows written as SQL exactly as an older install
// holds them.
//
// A schedule is seeded through raw SQL rather than `UpsertReportSchedule`,
// because that writer has already moved to `channel_id` and could not produce
// the shape being migrated FROM.
func migrateServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Server{store: st, auditDB: d}, dir
}

// execSQL runs SQL against the same file the Server's database has open.
func execSQL(t *testing.T, dir, q string, args ...any) {
	t.Helper()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// putBackRecipients re-creates the legacy column and lets it be filled.
//
// A FRESH DATABASE NO LONGER HAS IT — `schemaDDL` is the final shape — so the
// only way to test the migration is to put the old column back and then ask the
// migration to take it away again.
func putBackRecipients(t *testing.T, dir string) {
	t.Helper()
	execSQL(t, dir, `ALTER TABLE report_schedules ADD COLUMN recipients TEXT NOT NULL DEFAULT '[]'`)
}

func seedSchedule(t *testing.T, dir, id, name, recipients string) {
	t.Helper()
	execSQL(t, dir, `INSERT INTO report_schedules
		(id, router_id, name, sections, aggregate, channel_id, frequency, send_hour,
		 enabled, created_at, updated_at, recipients)
		VALUES (?, 'r1', ?, '["ping"]', 'day', '', 'weekly', 7, 1, 1, 1, ?)`,
		id, name, recipients)
}

// installMail gives the install a server for the carried lists to be attached to.
func installMail(t *testing.T, s *Server) {
	t.Helper()
	sealed, err := s.sealChannelConfig(smtpConfigJSON{
		Host: "mail.example.net", Port: 587, Secure: true, User: "u", Pass: "p",
		From: "md@example.net", To: "alerts@example.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{
		ID: "install-mail", Owner: db.InstallOwner, Name: "Email",
		Kind: notify.KindSMTP, Enabled: 1, Config: sealed, Events: "[]", Routers: "[]",
	}); err != nil {
		t.Fatal(err)
	}
}

// channelFor reads back the name and the CARRIED LIST of the channel a schedule
// now names.
//
// THE LIST IS IN Bcc, NOT To, and that is the privacy property this migration
// has to preserve — see `SeedReportChannels`. Reading To here would report the
// sending address and pass whatever the addresses did.
func channelFor(t *testing.T, s *Server, scheduleID string) (string, string) {
	t.Helper()
	rows, err := s.auditDB.ReportSchedulesFor("r1")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID != scheduleID {
			continue
		}
		if r.ChannelID == "" {
			return "", ""
		}
		c, found, cerr := s.auditDB.NotifyChannelByID(r.ChannelID)
		if cerr != nil || !found {
			t.Fatalf("%s names channel %q, which does not exist", scheduleID, r.ChannelID)
		}
		spec := notify.DecodeChannel(c.ID, c.Name, c.Kind, true,
			s.openChannelConfig(c.Config), c.Events, c.Routers, c.IfaceTypes, c.Tuning)
		bcc, _ := spec.Settings["smtpBcc"].(string)
		return c.Name, bcc
	}
	t.Fatalf("schedule %s vanished", scheduleID)
	return "", ""
}

// NOBODY'S REPORT CHANGES HANDS. This is the whole promise of the chosen
// grouping and the only one worth making: two schedules that mailed the same
// people share one channel, and a schedule that mailed different people gets its
// own.
func TestEveryDistinctRecipientListBecomesItsOwnChannel(t *testing.T) {
	s, dir := migrateServer(t)
	installMail(t, s)
	putBackRecipients(t, dir)

	seedSchedule(t, dir, "s1", "Weekly WAN", `["ops@example.net","noc@example.net"]`)
	seedSchedule(t, dir, "s2", "Monthly caps", `["ops@example.net","noc@example.net"]`)
	seedSchedule(t, dir, "s3", "Exec summary", `["cto@example.net"]`)
	// SAME PEOPLE, DIFFERENT ORDER AND CASE. Two deliveries that are identical
	// must not become two channels, or an operator who edits one is surprised
	// that the other did not follow.
	seedSchedule(t, dir, "s4", "Weekly WAN copy", `["NOC@example.net","Ops@example.net"]`)

	made, err := s.SeedReportChannels()
	if err != nil {
		t.Fatalf("SeedReportChannels: %v", err)
	}
	if made != 2 {
		t.Errorf("made %d channels, want 2 — one per DISTINCT list", made)
	}

	n1, to1 := channelFor(t, s, "s1")
	n2, to2 := channelFor(t, s, "s2")
	n3, to3 := channelFor(t, s, "s3")
	n4, _ := channelFor(t, s, "s4")

	if to1 != "ops@example.net, noc@example.net" {
		t.Errorf("s1's channel mails %q, not the people it used to", to1)
	}
	if n1 != n2 || to1 != to2 {
		t.Errorf("two schedules with one list got two channels: %q vs %q", n1, n2)
	}
	if n4 != n1 {
		t.Errorf("the same people in a different case and order made a second channel: %q", n4)
	}
	if to3 != "cto@example.net" || n3 == n1 {
		t.Errorf("s3 was merged into another channel: name=%q to=%q", n3, to3)
	}
	// THE FIRST SCHEDULE'S SPELLING IS WHAT THE CHANNEL KEEPS, because that is
	// what its operator typed.
	if to1 == "NOC@example.net, Ops@example.net" {
		t.Error("the later schedule's capitalisation overwrote the first one's")
	}

	// ── THE UPGRADE DISCLOSES NOBODY ────────────────────────────────────
	//
	// `reports.MailEnvelope` put every recipient of a schedule into Bcc and
	// addressed the message to the sending account, because these are frequently
	// different customers and a To list would show each of them all the others.
	// Carrying a list into To would have leaked every address on the first
	// upgrade, silently and to everyone at once. So: the addresses in Bcc, the
	// message addressed to the sender, and nothing in To or Cc.
	for _, id := range []string{"s1", "s3"} {
		rows, _ := s.auditDB.ReportSchedulesFor("r1")
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			c, _, _ := s.auditDB.NotifyChannelByID(r.ChannelID)
			spec := notify.DecodeChannel(c.ID, c.Name, c.Kind, true,
				s.openChannelConfig(c.Config), c.Events, c.Routers, c.IfaceTypes, c.Tuning)
			to, _ := spec.Settings["smtpTo"].(string)
			cc, _ := spec.Settings["smtpCc"].(string)
			if to != "md@example.net" {
				t.Errorf("%s's channel addresses %q; the carried list must be Bcc'd "+
					"with the message addressed to the sender, exactly as it was "+
					"before, or the upgrade shows every recipient to all the others",
					id, to)
			}
			if cc != "" {
				t.Errorf("%s's channel copies %q visibly", id, cc)
			}
		}
	}

	// THE SERVER IS COPIED FROM THE INSTALL'S MAIL CHANNEL, so a carried list can
	// actually be delivered rather than merely recorded.
	rows, _ := s.auditDB.ReportSchedulesFor("r1")
	for _, r := range rows {
		if _, _, ok := s.scheduleMail(r.ChannelID); !ok {
			t.Errorf("%s was carried onto a channel that cannot send", r.Name)
		}
	}
}

// IT RUNS ONCE. The dropped column is the latch, so a second start must find
// nothing to do and must not manufacture a second set of channels.
func TestTheCarryRunsOnceAndDropsTheColumn(t *testing.T) {
	s, dir := migrateServer(t)
	installMail(t, s)
	putBackRecipients(t, dir)
	seedSchedule(t, dir, "s1", "Weekly WAN", `["ops@example.net"]`)

	if _, err := s.SeedReportChannels(); err != nil {
		t.Fatal(err)
	}
	has, err := s.auditDB.HasReportRecipientsColumn()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("the recipients column survived, so this runs again every start and " +
			"the list has two homes that will disagree")
	}
	before, _ := s.auditDB.CountNotifyChannels()
	made, err := s.SeedReportChannels()
	if err != nil {
		t.Fatalf("a second run errored: %v", err)
	}
	after, _ := s.auditDB.CountNotifyChannels()
	if made != 0 || before != after {
		t.Errorf("a second run made %d channel(s) (%d -> %d)", made, before, after)
	}
}

// WITH NO MAIL SERVER, NOTHING IS CARRIED AND NOTHING IS DROPPED. An install
// with no channel to copy could not have been sending these reports, and
// destroying the lists would remove the only record of who they were for.
func TestWithNoMailChannelTheListsAreLeftAlone(t *testing.T) {
	s, dir := migrateServer(t)
	putBackRecipients(t, dir)
	seedSchedule(t, dir, "s1", "Weekly WAN", `["ops@example.net"]`)

	made, err := s.SeedReportChannels()
	if err != nil {
		t.Fatalf("SeedReportChannels: %v", err)
	}
	if made != 0 {
		t.Errorf("made %d channels with no mail server to copy", made)
	}
	has, err := s.auditDB.HasReportRecipientsColumn()
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("the recipient lists were destroyed with nowhere to put them")
	}
}

// AN EMPTY LIST GETS NO CHANNEL. It was sending to nobody, and inventing a
// destination would start mailing people who were never subscribed.
func TestAScheduleThatMailedNobodyGetsNoChannel(t *testing.T) {
	s, dir := migrateServer(t)
	installMail(t, s)
	putBackRecipients(t, dir)
	seedSchedule(t, dir, "s1", "Nobody", `[]`)
	seedSchedule(t, dir, "s2", "Somebody", `["ops@example.net"]`)

	made, err := s.SeedReportChannels()
	if err != nil {
		t.Fatal(err)
	}
	if made != 1 {
		t.Errorf("made %d channels, want 1", made)
	}
	if n, _ := channelFor(t, s, "s1"); n != "" {
		t.Errorf("a schedule with no recipients was given channel %q", n)
	}
	if n, _ := channelFor(t, s, "s2"); n == "" {
		t.Error("the schedule that DID have recipients was left without a channel")
	}
}

// THE CARRIED CHANNELS SUBSCRIBE TO NO ALERTS. They exist to receive a report,
// and an alert subscription would start mailing these people things they never
// asked for, on the first upgrade.
func TestACarriedChannelDeliversNoAlerts(t *testing.T) {
	s, dir := migrateServer(t)
	installMail(t, s)
	putBackRecipients(t, dir)
	seedSchedule(t, dir, "s1", "Weekly WAN", `["ops@example.net"]`)
	if _, err := s.SeedReportChannels(); err != nil {
		t.Fatal(err)
	}

	rows, err := s.auditDB.NotifyChannels()
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range rows {
		if c.ID == "install-mail" {
			continue
		}
		names = append(names, c.Name)
		spec := notify.DecodeChannel(c.ID, c.Name, c.Kind, true,
			s.openChannelConfig(c.Config), c.Events, c.Routers, c.IfaceTypes, c.Tuning)
		if spec.Wants("high_cpu", "r1") {
			t.Errorf("%q would deliver alerts to people who subscribed to a report", c.Name)
		}
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "Report recipients" {
		t.Errorf("carried channels are named %v", names)
	}
}
