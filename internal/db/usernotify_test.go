package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The table as the live migration creates it. Declared here rather than in a
// shared helper for the reason history_test.go gives about its own DDL: a helper
// that created every table would let a test pass while naming none of them.
const userNotifyDDL = `
CREATE TABLE user_notify_config (
  user_id    TEXT PRIMARY KEY,
  data       TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);`

func userNotifyDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.Exec(userNotifyDDL); err != nil {
		t.Fatal(err)
	}
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestUserNotifyConfigRoundTrips(t *testing.T) {
	d := userNotifyDB(t)

	if got, err := d.UserNotifyConfig("u1"); err != nil || got != nil {
		t.Fatalf("an unconfigured user read %v, %v", got, err)
	}

	want := map[string]any{
		"telegramEnabled": true,
		"telegramChatId":  "-100123",
		"ntfyUrl":         "https://ntfy.example/topic",
	}
	if err := d.SetUserNotifyConfig("u1", want); err != nil {
		t.Fatal(err)
	}
	got, err := d.UserNotifyConfig("u1")
	if err != nil {
		t.Fatal(err)
	}
	if got["telegramEnabled"] != true || got["telegramChatId"] != "-100123" ||
		got["ntfyUrl"] != "https://ntfy.example/topic" {
		t.Errorf("round trip lost data: %v", got)
	}

	// A second write REPLACES rather than merging: the caller owns the whole
	// object, and merging here would make a removed channel impossible to remove.
	if err := d.SetUserNotifyConfig("u1", map[string]any{"ntfyUrl": "https://other/t"}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.UserNotifyConfig("u1")
	if _, present := got["telegramChatId"]; present {
		t.Error("the second write merged with the first -- a removed channel could never be removed")
	}
	if got["ntfyUrl"] != "https://other/t" {
		t.Errorf("the second write did not take: %v", got)
	}
}

// TestOneUsersConfigIsNotAnothers. The row is keyed by user and the alert path
// reads it per recipient; a leak here would send one person's alerts to another
// person's destination.
func TestOneUsersConfigIsNotAnothers(t *testing.T) {
	d := userNotifyDB(t)
	if err := d.SetUserNotifyConfig("alice", map[string]any{"emailTo": "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserNotifyConfig("bob", map[string]any{"emailTo": "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	a, _ := d.UserNotifyConfig("alice")
	b, _ := d.UserNotifyConfig("bob")
	if a["emailTo"] != "alice@example.com" || b["emailTo"] != "bob@example.com" {
		t.Errorf("configs crossed: alice=%v bob=%v", a, b)
	}
	if c, _ := d.UserNotifyConfig("carol"); c != nil {
		t.Errorf("an unknown user read somebody's config: %v", c)
	}
}

// TestACorruptBlobReadsAsNotConfigured.
//
// This is read from inside the alert path. An error there would take down
// delivery for every OTHER recipient too, so one user's unreadable row must cost
// that user their alerts and nobody else theirs.
func TestACorruptBlobReadsAsNotConfigured(t *testing.T) {
	d := userNotifyDB(t)
	if err := d.SetUserNotifyConfig("u1", map[string]any{"emailTo": "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(
		`UPDATE user_notify_config SET data = ? WHERE user_id = ?`, "{not json", "u1"); err != nil {
		t.Fatal(err)
	}

	got, err := d.UserNotifyConfig("u1")
	if err != nil {
		t.Errorf("a corrupt blob returned an error (%v) -- that would abort delivery "+
			"for every other recipient in the same alert", err)
	}
	if got != nil {
		t.Errorf("a corrupt blob produced %v", got)
	}
}

// TestRemovingAUsersConfig: a personal destination must not outlive the account
// that owns it. A deleted user's stored ntfy URL is still a host this server
// would connect to.
func TestRemovingAUsersConfig(t *testing.T) {
	d := userNotifyDB(t)
	if err := d.SetUserNotifyConfig("u1", map[string]any{"ntfyUrl": "https://ntfy/x"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveUserNotifyConfig("u1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.UserNotifyConfig("u1"); got != nil {
		t.Errorf("the config survived the user: %v", got)
	}
	// Removing what is not there is not an error -- deletion is called on every
	// user removal, most of whom never configured a channel.
	if err := d.RemoveUserNotifyConfig("never-existed"); err != nil {
		t.Errorf("removing an absent config errored: %v", err)
	}
	if err := d.RemoveUserNotifyConfig(""); err != nil {
		t.Errorf("removing an empty user id errored: %v", err)
	}
}

func TestUserNotifyRefusesAnEmptyUser(t *testing.T) {
	d := userNotifyDB(t)
	if err := d.SetUserNotifyConfig("", map[string]any{"emailTo": "x@example.com"}); err == nil {
		t.Error("a config was stored against no user at all")
	}
	if got, err := d.UserNotifyConfig(""); err != nil || got != nil {
		t.Errorf("reading an empty user id gave %v, %v", got, err)
	}
}
