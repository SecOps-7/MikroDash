package server

import (
	"testing"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
)

// TestASocketWriteRecordsWhoMadeIt. The WebSocket recorder passed "" for the
// actor id while the HTTP one resolved it, so every router write made from a
// page (all of them go over the socket) stored actor_id NULL:
// audit_events.actor_id holds the USER ID (CLAUDE.md), and a filter or join on
// it found none of them (review loop).
func TestASocketWriteRecordsWhoMadeIt(t *testing.T) {
	cn := connFor(t, testResolver(t), "r-A") // userID u-1
	cn.recorder().Record(audit.Event{Action: "res.update", Scope: "router", RouterID: "r-A",
		TargetType: "dnsStatic", TargetName: "x"})

	page, err := cn.srv.auditDB.QueryAuditEvents(db.Query{RouterIDs: []string{"r-A"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("%d audit rows, want the one just recorded", len(page.Rows))
	}
	if got := page.Rows[0].ActorID; got == nil || *got != "u-1" {
		t.Errorf("actor_id is %v, want the connection's user id u-1", got)
	}
	if got := page.Rows[0].ActorName; got != "someone" {
		t.Errorf("actor_name is %q, want the username: the control", got)
	}
}
