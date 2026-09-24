package server

import (
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/notify"
)

// WHICH MAIL CHANNEL A SCHEDULED REPORT LEAVES THROUGH.
//
// This rule is implemented TWICE — here, and in `reportsChannelID()` in
// web/src/pages/settings-notify-channels.ts, which ticks one SMTP card. That is
// deliberate: the page has to draw the answer before any report is due, and it
// cannot ask the server to evaluate a rule over rows it already holds. But two
// implementations of one rule drift silently, and the drift is invisible — the
// page shows a tick on one channel while reports go out through another, and
// nothing anywhere disagrees out loud.
//
// So the cases below are the SAME cases the web test asserts, in the same order,
// with the same names. A change to either side that is not made to both shows up
// as one suite passing and the other failing, rather than as an operator
// wondering why the report came from the wrong address.
func TestWhichChannelTheReportsLeaveThrough(t *testing.T) {
	smtp := func(id, owner string, enabled int) db.NotifyChannel {
		return db.NotifyChannel{ID: id, Owner: owner, Kind: notify.KindSMTP, Enabled: enabled}
	}
	hook := func(id string) db.NotifyChannel {
		return db.NotifyChannel{ID: id, Owner: db.InstallOwner, Kind: notify.KindWebhook, Enabled: 1}
	}

	for _, tc := range []struct {
		name string
		rows []db.NotifyChannel
		want string
		id   string
	}{
		{
			"none stored takes the first enabled install SMTP channel",
			[]db.NotifyChannel{smtp("m1", db.InstallOwner, 1), smtp("m2", db.InstallOwner, 1)},
			"", "m1",
		},
		{
			"a stored choice is taken instead of the first",
			[]db.NotifyChannel{smtp("m1", db.InstallOwner, 1), smtp("m2", db.InstallOwner, 1)},
			"m2", "m2",
		},
		{
			// THE SILENT ONE. Delete the chosen channel and the stored id names
			// nothing. Without the second pass this returned no channel at all
			// and every scheduled report stopped, while the page carried on
			// showing a configured mail server.
			"a stored id naming a deleted channel falls through to the first",
			[]db.NotifyChannel{smtp("m1", db.InstallOwner, 1), smtp("m2", db.InstallOwner, 1)},
			"gone", "m1",
		},
		{
			"a webhook is never chosen, because it cannot carry a PDF",
			[]db.NotifyChannel{hook("w1"), smtp("m1", db.InstallOwner, 1)},
			"", "m1",
		},
		{
			// A user's personal email channel must not become the server every
			// scheduled report in the install goes out through.
			"a user-owned mail channel is not the install's reports sender",
			[]db.NotifyChannel{smtp("mine", "user:7", 1), smtp("m1", db.InstallOwner, 1)},
			"", "m1",
		},
		{
			"a disabled install channel is skipped by the fallback",
			[]db.NotifyChannel{smtp("off", db.InstallOwner, 0), smtp("m1", db.InstallOwner, 1)},
			"", "m1",
		},
		{
			"no SMTP channel at all means no mail server, not a webhook",
			[]db.NotifyChannel{hook("w1")},
			"", "",
		},
		{
			// THE TWO SIDES DISAGREED HERE, which is exactly the drift this
			// file's header warns about. The page filters to install-owned SMTP
			// BEFORE looking for the stored id, so it would tick m1; the server
			// matched the id against any SMTP channel and took the user's. The
			// page never offers a user-owned channel, so a stored id naming one
			// arrived some other way and is not honoured for being specific.
			"a stored id naming a USER's mail channel is not honoured",
			[]db.NotifyChannel{smtp("mine", "user:7", 1), smtp("m1", db.InstallOwner, 1)},
			"mine", "m1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseReportChannel(tc.rows, tc.want)
			id := ""
			if got != nil {
				id = got.ID
			}
			if id != tc.id {
				t.Errorf("chose %q, want %q — the report would leave through the "+
					"wrong mail server, or none, with the Settings page ticking "+
					"a different card", id, tc.id)
			}
		})
	}
}

// THE NAMED CHANNEL IS TAKEN EVEN WHEN DISABLED, and that is on purpose rather
// than an oversight: the page will not let a disabled channel be ticked, so a
// stored id pointing at one came from somewhere deliberate — an operator turning
// the channel off without moving the reports. Silently re-routing their reports
// to a different mail server would be worse than not sending them, because the
// mail arrives and looks right.
func TestADisabledNamedChannelIsStillTheNamedChannel(t *testing.T) {
	rows := []db.NotifyChannel{
		{ID: "m1", Owner: db.InstallOwner, Kind: notify.KindSMTP, Enabled: 1},
		{ID: "m2", Owner: db.InstallOwner, Kind: notify.KindSMTP, Enabled: 0},
	}
	got := chooseReportChannel(rows, "m2")
	if got == nil || got.ID != "m2" {
		t.Errorf("a disabled but explicitly chosen channel was replaced by %v — "+
			"reports would quietly start coming from another mail server", got)
	}
}
