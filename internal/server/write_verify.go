package server

import (
	"errors"

	"mikrodash/internal/audit"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
)

// ── A WRITE IS CONFIRMED BY READING IT BACK (#97) ───────────────────────────
//
// RouterOS answering `!done` to /add, /set, /remove or /move says the command was
// accepted, not what the table now holds. The save path reported success and
// audited the REQUESTED values on that answer alone: an edit was never read back,
// and a create whose new row could not be found still said "ok" and skipped undo.
//
// Each write now re-reads the menu and checks its own postcondition before it
// reports success, and the audit records the row as observed. When the read-back
// fails or the postcondition does not hold, the write is not called a failure (it
// may well have happened) but "outcome unknown": the table is refreshed and the
// operator is told to check it.

var errOutcomeUnknown = errors.New("the router accepted the change, but the result could not be confirmed; the table has been refreshed")

// rowByID is the row with this id, or nil.
func rowByID(rows []routeros.Reply, id string) routeros.Reply {
	for _, r := range rows {
		if r[".id"] == id {
			return r
		}
	}
	return nil
}

// confirmCreated is the ONE row that was not there before. None means the add left
// nothing to find; more than one means something else added a row at the same
// moment, and which is ours cannot be told. Both are unknown outcomes.
func confirmCreated(before map[string]bool, after []routeros.Reply) (routeros.Reply, bool) {
	var found routeros.Reply
	for _, r := range after {
		if before[r[".id"]] {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = r
	}
	return found, found != nil
}

// confirmRemoved: the row is gone.
func confirmRemoved(after []routeros.Reply, id string) bool {
	return rowByID(after, id) == nil
}

// confirmMoved: RouterOS places a moved row BEFORE its destination, or last when
// there is none. Returns where the row now is.
func confirmMoved(after []routeros.Reply, id, dest string) (int, bool) {
	at := -1
	for i, r := range after {
		if r[".id"] == id {
			at = i
			break
		}
	}
	if at < 0 {
		return -1, false
	}
	if dest == "" {
		return at, at == len(after)-1
	}
	return at, at+1 < len(after) && after[at+1][".id"] == dest
}

// confirmAction: the row is still there, and an enable or disable took.
func confirmAction(after []routeros.Reply, id, verb string) (routeros.Reply, bool) {
	row := rowByID(after, id)
	if row == nil {
		return nil, false
	}
	switch verb {
	case "enable":
		return row, row["disabled"] != "true"
	case "disable":
		return row, row["disabled"] == "true"
	}
	return row, true
}

// observedValues is the row as read back, for the audit trail: what the router
// holds, not what was asked for. A secret never reads back, so a secret the write
// set is carried over from the request, and auditValues records it as set.
func observedValues(res *resource.Resource, row routeros.Reply, requested map[string]string) map[string]any {
	obs := res.RowValues(row)
	for _, f := range res.Fields {
		if f.Type != resource.TypeSecret {
			continue
		}
		if v, ok := requested[f.Name]; ok {
			obs[f.Name] = v
		}
	}
	return obs
}

// outcomeUnknown answers a write the router accepted but the read-back could not
// confirm. It is not a failure, because the change may well have happened:
//
//   - this resource's undo history is dropped, since it may now point at rows
//     that are not what it recorded;
//   - the attempt is audited with a note saying so, without an After it cannot
//     vouch for;
//   - the table is refreshed, so the page shows what the router really has;
//   - the page is told `outcome-unknown` rather than ok or failed.
//
// It returns nil so the caller's write-queue error path does not report twice.
func (cn *conn) outcomeUnknown(res *resource.Resource, action, id, name, ack string) error {
	cn.histDrop(res.Key)
	cn.recorder().Record(audit.Event{
		Action: action, TargetType: res.Key, RouterID: cn.routerID,
		TargetID: id, TargetName: name,
		Note:  "outcome-unknown: the router accepted the change, but reading it back did not confirm it",
		Extra: ackExtra(ack),
	})
	cn.refreshFor(res)
	cn.resErr(res.Key, "outcome-unknown", name, map[string]any{"message": safe.Message(errOutcomeUnknown.Error())})
	return nil
}
