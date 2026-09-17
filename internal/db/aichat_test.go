package db

import (
	"fmt"
	"testing"
)

// THE LAST N, IN THE ORDER THEY WERE SAID. Both halves fail visibly here: an
// ascending LIMIT returns the first turns, and a descending result with no outer
// sort hands the model the conversation backwards.
func TestRecentAIMessagesAreTheLatestOldestFirst(t *testing.T) {
	d := openTestDB(t)
	for i := 1; i <= 6; i++ {
		role := AIRoleUser
		if i%2 == 0 {
			role = AIRoleAssistant
		}
		// Same millisecond is likely for rows written this fast, which is what
		// the `id` tie-break exists for.
		if err := d.AppendAIMessage("u1", "r1", role, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.RecentAIMessages("u1", "r1", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"m3", "m4", "m5", "m6"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, m := range got {
		if m.Text != want[i] {
			t.Errorf("row %d = %q, want %q (full: %+v)", i, m.Text, want[i], got)
		}
	}
	if got[0].Role != AIRoleUser || got[1].Role != AIRoleAssistant {
		t.Errorf("roles did not survive: %+v", got)
	}
}

// ONE PERSON'S THREAD ABOUT ONE ROUTER. Read and delete are both checked against
// a neighbour on each axis, so a query that forgot either half of the key shows.
func TestAIThreadsAreScopedToUserAndRouter(t *testing.T) {
	d := openTestDB(t)
	for _, k := range [][2]string{{"u1", "r1"}, {"u1", "r2"}, {"u2", "r1"}} {
		if err := d.AppendAIMessage(k[0], k[1], AIRoleUser, k[0]+"@"+k[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := d.RecentAIMessages("u1", "r1", 10)
	if len(got) != 1 || got[0].Text != "u1@r1" {
		t.Fatalf("u1@r1 read %+v", got)
	}

	n, err := d.DeleteAIThread("u1", "r1")
	if err != nil || n != 1 {
		t.Fatalf("delete removed %d rows (err %v), want exactly 1", n, err)
	}
	if left, _ := d.RecentAIMessages("u1", "r1", 10); len(left) != 0 {
		t.Errorf("the cleared thread still reads %+v", left)
	}
	for _, k := range [][2]string{{"u1", "r2"}, {"u2", "r1"}} {
		if left, _ := d.RecentAIMessages(k[0], k[1], 10); len(left) != 1 {
			t.Errorf("clearing u1@r1 touched %s@%s: %+v", k[0], k[1], left)
		}
	}
}

func TestAppendAIMessageRefusesWhatItCannotFile(t *testing.T) {
	d := openTestDB(t)
	if err := d.AppendAIMessage("", "r1", AIRoleUser, "x"); err == nil {
		t.Error("a message with no user was accepted")
	}
	if err := d.AppendAIMessage("u1", "", AIRoleUser, "x"); err == nil {
		t.Error("a message with no router was accepted")
	}
	if err := d.AppendAIMessage("u1", "r1", "system", "x"); err == nil {
		t.Error("a system-role row was accepted; only visible turns are kept")
	}
	if err := d.AppendAIMessage("u1", "r1", AIRoleUser, ""); err != nil {
		t.Errorf("an empty turn should be skipped quietly, got %v", err)
	}
	if got, _ := d.RecentAIMessages("u1", "r1", 10); len(got) != 0 {
		t.Errorf("rows were written: %+v", got)
	}
}
