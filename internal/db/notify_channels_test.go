package db

import (
	"testing"
)

func seedChannel(t *testing.T, d *DB, id, owner, name string) NotifyChannel {
	t.Helper()
	c := NotifyChannel{
		ID: id, Owner: owner, Name: name, Kind: "webhook", Enabled: 1,
		Config:    `{"urls":"SEALED"}`,
		Events:    `["ping_loss","high_cpu"]`,
		Routers:   `[]`,
		CreatedBy: "u1", CreatedAt: 1000, UpdatedAt: 1000,
	}
	if err := d.UpsertNotifyChannel(c); err != nil {
		t.Fatalf("UpsertNotifyChannel: %v", err)
	}
	return c
}

// THE ROUND TRIP IS READ BACK FROM THE TABLE, not compared with what was
// written. A writer that put the wrong value in a column agrees with itself
// whatever it wrote; only the read can disagree.
func TestANotifyChannelSurvivesTheRoundTrip(t *testing.T) {
	d := openTest(t, t.TempDir())
	want := seedChannel(t, d, "c1", InstallOwner, "Ops Telegram")

	got, ok, err := d.NotifyChannelByID("c1")
	if err != nil || !ok {
		t.Fatalf("NotifyChannelByID: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("read back\n%+v\nwrote\n%+v", got, want)
	}
}

func TestAnUnknownChannelIsNotAnError(t *testing.T) {
	d := openTest(t, t.TempDir())
	// The API needs "no such row" told apart from "the read failed", so it can
	// answer 404 rather than 500.
	_, ok, err := d.NotifyChannelByID("nope")
	if err != nil {
		t.Fatalf("an absent channel reported an error: %v", err)
	}
	if ok {
		t.Error("an absent channel reported as found")
	}
}

// AN EDIT MUST NOT REWRITE OWNERSHIP. A channel that changed hands on an edit
// would let a user re-point their own channel at the install, escaping the
// permission that decided where it could live.
func TestAnEditCannotChangeOwnerOrAuthorship(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedChannel(t, d, "c1", "u1", "Mine")

	stolen := NotifyChannel{
		ID: "c1", Owner: InstallOwner, Name: "Renamed", Kind: "smtp", Enabled: 0,
		Config: `{"host":"mail.example.net"}`, Events: `["host_down"]`,
		Routers: `["r1"]`, CreatedBy: "u2", CreatedAt: 9999, UpdatedAt: 2000,
	}
	if err := d.UpsertNotifyChannel(stolen); err != nil {
		t.Fatalf("UpsertNotifyChannel: %v", err)
	}

	got, _, err := d.NotifyChannelByID("c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "u1" {
		t.Errorf("owner became %q — an edit moved the channel to another owner", got.Owner)
	}
	if got.CreatedBy != "u1" {
		t.Errorf("created_by became %q — an edit rewrote who made it", got.CreatedBy)
	}
	if got.CreatedAt != 1000 {
		t.Errorf("created_at became %d — an edit rewrote when it was made", got.CreatedAt)
	}
	// And everything that IS editable did change, or this test would pass on a
	// write that did nothing at all.
	if got.Name != "Renamed" || got.Kind != "smtp" || got.Enabled != 0 ||
		got.Events != `["host_down"]` || got.Routers != `["r1"]` || got.UpdatedAt != 2000 {
		t.Errorf("the editable fields did not update: %+v", got)
	}
}

func TestChannelsAreListedByOwnerAndInFull(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedChannel(t, d, "c1", InstallOwner, "Bravo")
	seedChannel(t, d, "c2", "u1", "Alpha")

	all, err := d.NotifyChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("NotifyChannels returned %d, want 2", len(all))
	}
	// Name order, so the list does not shuffle between reads.
	if all[0].Name != "Alpha" || all[1].Name != "Bravo" {
		t.Errorf("not in name order: %q, %q", all[0].Name, all[1].Name)
	}

	mine, err := d.NotifyChannelsFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].ID != "c2" {
		t.Errorf("NotifyChannelsFor(u1) = %+v, want just c2", mine)
	}
}

// AN EMPTY LIST IS AN EMPTY ARRAY, never nil: a nil slice marshals to `null` and
// the browser's render would have to defend against it.
func TestAnEmptyChannelListIsNotNil(t *testing.T) {
	d := openTest(t, t.TempDir())
	all, err := d.NotifyChannels()
	if err != nil {
		t.Fatal(err)
	}
	if all == nil {
		t.Error("NotifyChannels returned nil, which reaches the browser as null")
	}
	mine, err := d.NotifyChannelsFor("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if mine == nil {
		t.Error("NotifyChannelsFor returned nil for an owner with no channels")
	}
}

func TestDeleteReportsWhetherItRemovedAnything(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedChannel(t, d, "c1", InstallOwner, "One")

	gone, err := d.DeleteNotifyChannel("c1")
	if err != nil || !gone {
		t.Fatalf("DeleteNotifyChannel: gone=%v err=%v", gone, err)
	}
	// A SECOND DELETE IS NOT AN ERROR AND NOT A SUCCESS. The API answers 404 on
	// the false, so the two must be told apart.
	gone, err = d.DeleteNotifyChannel("c1")
	if err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
	if gone {
		t.Error("deleting an absent channel reported that it removed one")
	}
}

func TestCountIsWhatTheMigrationAsks(t *testing.T) {
	d := openTest(t, t.TempDir())
	n, err := d.CountNotifyChannels()
	if err != nil || n != 0 {
		t.Fatalf("a fresh install has %d channels, err=%v", n, err)
	}
	seedChannel(t, d, "c1", InstallOwner, "One")
	if n, err = d.CountNotifyChannels(); err != nil || n != 1 {
		t.Fatalf("after one insert: %d, err=%v", n, err)
	}
}
