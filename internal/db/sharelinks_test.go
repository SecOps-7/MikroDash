package db

import (
	"bytes"
	"testing"
	"time"
)

// Migration 40: the share-link tables, on a database that predates them.
func TestMigrationFortyAddsTheShareLinkTables(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, stmt := range []string{
		`DROP TABLE share_link_slides`,
		`DROP TABLE share_links`,
		`DELETE FROM schema_version WHERE version >= 40`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := d.ShareLinks(); err == nil {
		t.Fatal("share_links still answers after it was dropped; the wind-back did not take")
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v != schemaVersion || schemaVersion < 40 {
		t.Errorf("schema is v%d after Migrate (constant %d), want the current version, at least 40", v, schemaVersion)
	}
	if links, err := d.ShareLinks(); err != nil || len(links) != 0 {
		t.Errorf("after the migration: %v %v", links, err)
	}
}

func sampleLink(id string, hash string) ShareLink {
	exp := int64(1_900_000_000_000)
	return ShareLink{
		ID: id, TokenHash: []byte(hash), Label: "Lobby wall screen", Kind: "page",
		Page: "dashboard", RouterID: "r1", Layout: `[{"id":"card-traffic"}]`,
		Options: `{"theme":"dark","view":"embed"}`, FrameOrigins: []string{"https://ha.example.com"},
		ViewerCap: 8, AllowedCIDRs: []string{"198.51.100.0/24"}, CreatedBy: "u-1",
		CreatedAt: 1_800_000_000_000, ExpiresAt: &exp,
	}
}

func TestAShareLinkRoundTrips(t *testing.T) {
	d := openTest(t, t.TempDir())
	in := sampleLink("l1", "hash-1")
	if err := d.InsertShareLink(in); err != nil {
		t.Fatal(err)
	}
	got, err := d.ShareLinkByHash([]byte("hash-1"))
	if err != nil || got == nil {
		t.Fatalf("by hash: %v %v", got, err)
	}
	if got.Label != in.Label || got.Page != "dashboard" || got.RouterID != "r1" || got.ViewerCap != 8 ||
		got.Options != in.Options || got.Layout != in.Layout || got.CreatedBy != "u-1" ||
		got.ExpiresAt == nil || *got.ExpiresAt != *in.ExpiresAt || got.RevokedAt != nil ||
		len(got.FrameOrigins) != 1 || got.AllowedCIDRs[0] != "198.51.100.0/24" ||
		!bytes.Equal(got.TokenHash, []byte("hash-1")) {
		t.Errorf("round trip: %+v", got)
	}
	if miss, err := d.ShareLinkByHash([]byte("nope")); err != nil || miss != nil {
		t.Errorf("an unknown hash: %v %v", miss, err)
	}
	// NEVER EXPIRES is a NULL expiry.
	never := sampleLink("l2", "hash-2")
	never.ExpiresAt = nil
	never.ViewerCap = 0
	if err := d.InsertShareLink(never); err != nil {
		t.Fatal(err)
	}
	n, _ := d.ShareLinkByID("l2")
	if n == nil || n.ExpiresAt != nil || n.ViewerCap != 0 {
		t.Errorf("a never-expiring link: %+v", n)
	}
	// The hash is unique: a second link with the same hash is refused.
	if err := d.InsertShareLink(sampleLink("l3", "hash-1")); err == nil {
		t.Error("two links with one token hash were stored")
	}
}

func TestASlideshowKeepsItsSlidesInOrder(t *testing.T) {
	d := openTest(t, t.TempDir())
	l := sampleLink("s1", "hash-s1")
	l.Kind, l.Page, l.RouterID = "slideshow", "", ""
	l.Slides = []ShareSlide{{Page: "dashboard", RouterID: "r1", Seconds: 45}, {Page: "bandwidth", RouterID: "r2"},
		{Page: "wifi-clients", RouterID: "r1"}}
	if err := d.InsertShareLink(l); err != nil {
		t.Fatal(err)
	}
	got, _ := d.ShareLinkByID("s1")
	if got == nil || len(got.Slides) != 3 || got.Slides[0].Seconds != 45 || got.Slides[1].Page != "bandwidth" ||
		got.Slides[2].RouterID != "r1" {
		t.Fatalf("slides: %+v", got)
	}
	if ids := got.RouterIDs(); len(ids) != 2 {
		t.Errorf("routers shown: %v", ids)
	}
	for _, r := range []string{"r1", "r2"} {
		ids, err := d.ActiveShareLinkIDsForRouter(r)
		if err != nil || len(ids) != 1 || ids[0] != "s1" {
			t.Errorf("links showing %s: %v %v", r, ids, err)
		}
	}
	// An edit replaces the slides; the order is the new one.
	got.Slides = []ShareSlide{{Page: "logs", RouterID: "r2"}, {Page: "dashboard", RouterID: "r2"}}
	if err := d.UpdateShareLink(*got); err != nil {
		t.Fatal(err)
	}
	again, _ := d.ShareLinkByID("s1")
	if len(again.Slides) != 2 || again.Slides[0].Page != "logs" {
		t.Errorf("after the edit: %+v", again.Slides)
	}
	if ids, _ := d.ActiveShareLinkIDsForRouter("r1"); len(ids) != 0 {
		t.Errorf("r1 is no longer in the slideshow but still finds it: %v", ids)
	}
	// Deleting the link removes its slides with it.
	if ok, err := d.DeleteShareLink("s1"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM share_link_slides`).Scan(&n)
	if n != 0 {
		t.Errorf("%d slides outlived their link", n)
	}
}

func TestARevocationIsFinal(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.InsertShareLink(sampleLink("l1", "h")); err != nil {
		t.Fatal(err)
	}
	ok, err := d.RevokeShareLink("l1", "u-2", "admin", 5)
	if !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	// A second revocation (say an automatic one racing the admin) changes nothing.
	if ok, _ := d.RevokeShareLink("l1", "", "creator-not-admin", 9); ok {
		t.Error("a revoked link was revoked again, overwriting who and why")
	}
	got, _ := d.ShareLinkByID("l1")
	if got.RevokedAt == nil || *got.RevokedAt != 5 || got.RevokedBy != "u-2" || got.RevokeReason != "admin" {
		t.Errorf("revocation: %+v", got)
	}
	// A revoked link cannot be edited.
	got.Label = "changed"
	if err := d.UpdateShareLink(*got); err == nil {
		t.Error("a revoked link was edited")
	}
	if ids, _ := d.ActiveShareLinkIDsForRouter("r1"); len(ids) != 0 {
		t.Errorf("a revoked link is still active for its router: %v", ids)
	}
	if err := d.TouchShareLink("l1", 7); err != nil {
		t.Fatal(err)
	}
}

// TestShareLinkRetention: a link 90 days past its revocation or its expiry
// is pruned; an active never-expiring link never is.
func TestShareLinkRetention(t *testing.T) {
	d := openTest(t, t.TempDir())
	now := time.Now()
	old := now.Add(-91 * 24 * time.Hour).UnixMilli()
	recent := now.Add(-89 * 24 * time.Hour).UnixMilli()
	mk := func(id string, exp *int64) {
		l := sampleLink(id, "h-"+id)
		l.ExpiresAt = exp
		l.CreatedAt = old
		if err := d.InsertShareLink(l); err != nil {
			t.Fatal(err)
		}
	}
	mk("never-active", nil)
	mk("expired-long-ago", &old)
	mk("expired-recently", &recent)
	mk("revoked-long-ago", nil)
	mk("revoked-recently", nil)
	_, _ = d.RevokeShareLink("revoked-long-ago", "u-1", "admin", old)
	_, _ = d.RevokeShareLink("revoked-recently", "u-1", "admin", recent)
	if _, err := d.Prune(PruneDays{}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"never-active": true, "expired-recently": true, "revoked-recently": true}
	links, _ := d.ShareLinks()
	if len(links) != len(want) {
		t.Errorf("%d links left, want %d: %+v", len(links), len(want), links)
	}
	for _, l := range links {
		if !want[l.ID] {
			t.Errorf("%s survived the prune", l.ID)
		}
	}
}
